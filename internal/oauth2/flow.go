package oauth2

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/absonsnet/daak/internal/logging"
	"github.com/rs/zerolog"
)

// AuthSession represents an in-progress OAuth authorization flow
type AuthSession struct {
	Provider       string          `json:"provider"`
	State          string          `json:"state"`
	CodeVerifier   string          `json:"codeVerifier"` // PKCE
	RedirectPort   int             `json:"redirectPort"`
	CreatedAt      time.Time       `json:"createdAt"`
	ProviderConfig *ProviderConfig `json:"-"` // Optional custom provider config
}

// TokenResponse represents the response from an OAuth token endpoint
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"` // Seconds until expiry
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token,omitempty"` // OpenID Connect
}

// DefaultTokenLifetimeSeconds is the assumed lifetime for a token whose
// provider did not state one (ExpiresIn missing or zero). Storing such a
// token as "expires now" made every use trigger a refresh — and, with
// rotating refresh tokens, a refresh storm. One hour is the common
// provider lifetime (Google, Microsoft) and a sane fallback.
const DefaultTokenLifetimeSeconds = 3600

// ExpiryFromNow converts a token endpoint's expires_in into an absolute
// expiry time. A missing or zero lifetime defaults to
// DefaultTokenLifetimeSeconds instead of "already expired".
func ExpiryFromNow(expiresIn int) time.Time {
	if expiresIn <= 0 {
		expiresIn = DefaultTokenLifetimeSeconds
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second)
}

// UserInfo represents basic user information from ID token or userinfo endpoint
type UserInfo struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

// Manager handles OAuth2 authorization flows. It holds at most one active
// flow at a time: mail setup, contacts-only setup and the extension Auth
// Broker share the one Manager, so starting a second flow while one is
// active is rejected instead of killing the first flow's callback server
// (audit finding 24). mu guards activeSession and callbackServer.
type Manager struct {
	log            zerolog.Logger
	mu             sync.Mutex
	activeSession  *AuthSession
	callbackServer *CallbackServer
	httpClient     *http.Client
}

// NewManager creates a new OAuth2 manager
func NewManager() *Manager {
	return &Manager{
		log: logging.WithComponent("oauth2"),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// StartAuthFlow begins the OAuth2 authorization code flow with PKCE
// Returns the authorization URL that should be opened in the user's browser
func (m *Manager) StartAuthFlow(ctx context.Context, providerName string) (string, error) {
	// Check if provider is configured
	if !IsProviderConfigured(providerName) {
		return "", fmt.Errorf("OAuth provider %s is not configured (missing client ID)", providerName)
	}

	provider, err := GetProvider(providerName)
	if err != nil {
		return "", err
	}

	return m.startAuthFlowInternal(ctx, providerName, provider, nil)
}

// StartAuthFlowWithProvider begins OAuth2 flow with a custom provider config
// Used for contacts-only OAuth flows that have different scopes
func (m *Manager) StartAuthFlowWithProvider(ctx context.Context, provider *ProviderConfig) (string, error) {
	if provider.ClientID == "" {
		return "", fmt.Errorf("OAuth provider %s is not configured (missing client ID)", provider.Name)
	}

	return m.startAuthFlowInternal(ctx, provider.Name, *provider, provider)
}

// startAuthFlowInternal is the common implementation for starting OAuth flows.
// It rejects a new flow while one is already active instead of cancelling
// the in-flight flow: the previous behaviour unconditionally cancelled the
// existing session, which killed the first flow's callback server and
// surfaced a generic oauth:error to its waiter (audit finding 24).
func (m *Manager) startAuthFlowInternal(ctx context.Context, providerName string, provider ProviderConfig, customConfig *ProviderConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeSession != nil {
		return "", fmt.Errorf("OAuth authorization flow already in progress for provider %q: finish or cancel the current sign-in before starting another one", m.activeSession.Provider)
	}
	// Defensive: no session should exist without a server, and vice versa.
	// If a previous flow's server somehow outlived its session, stop it
	// here rather than leaking the port.
	if m.callbackServer != nil {
		m.callbackServer.Stop()
		m.callbackServer = nil
	}

	// Generate PKCE code verifier and challenge
	verifier, err := generateCodeVerifier()
	if err != nil {
		return "", fmt.Errorf("failed to generate code verifier: %w", err)
	}
	challenge := generateCodeChallenge(verifier)

	// Generate state for CSRF protection
	state, err := generateState()
	if err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}

	// Start callback server
	server := NewCallbackServer()
	port, err := server.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to start callback server: %w", err)
	}
	m.callbackServer = server

	// Create session
	m.activeSession = &AuthSession{
		Provider:       providerName,
		State:          state,
		CodeVerifier:   verifier,
		RedirectPort:   port,
		CreatedAt:      time.Now(),
		ProviderConfig: customConfig,
	}

	// Build authorization URL
	authURL := buildAuthURL(provider, state, challenge, port)

	m.log.Info().
		Str("provider", providerName).
		Int("port", port).
		Msg("Started OAuth authorization flow")

	return authURL, nil
}

// WaitForCallback waits for the OAuth callback and exchanges the code for tokens
// Returns the tokens and user email on success. The active session and its
// callback server are cleared on EVERY exit path — success, callback
// failure, state mismatch, token-exchange failure — so a failed flow never
// leaves a stale session (and port) behind that blocks the next sign-in
// (audit finding 24). The cleanup is identity-checked (see
// clearSessionFor): the deferred clear must only drop the session this
// call waited on. A plain clear here would wipe a NEW flow started
// after a cancel — CancelAuthFlow clears the session at once, so a
// retry can start while this waiter is still unwinding, and the old
// waiter's deferred clear would otherwise stop the new flow's server
// and nil its session (re-audit finding R13).
func (m *Manager) WaitForCallback(ctx context.Context) (*TokenResponse, string, error) {
	m.mu.Lock()
	if m.activeSession == nil || m.callbackServer == nil {
		m.mu.Unlock()
		return nil, "", fmt.Errorf("no active OAuth session")
	}
	session := m.activeSession
	server := m.callbackServer
	m.mu.Unlock()

	// Clear this flow's session/server on every exit path. The wait
	// itself runs outside mu so CancelAuthFlow and a (rejected)
	// concurrent Start can still observe the in-progress flow while
	// we block here. Identity-checked: only this session/server pair
	// is cleared, never a newer flow's (re-audit finding R13).
	defer m.clearSessionFor(session, server)

	// Use custom provider config if available, otherwise look up by name
	var provider ProviderConfig
	var err error
	if session.ProviderConfig != nil {
		provider = *session.ProviderConfig
	} else {
		provider, err = GetProvider(session.Provider)
		if err != nil {
			return nil, "", err
		}
	}

	// Wait for callback
	result, err := server.WaitForCallback(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("callback failed: %w", err)
	}

	// Verify state
	if result.State != session.State {
		return nil, "", fmt.Errorf("state mismatch: possible CSRF attack")
	}

	// Check for error
	if result.Error != "" {
		return nil, "", fmt.Errorf("OAuth error: %s - %s", result.Error, result.ErrorDescription)
	}

	// Exchange code for tokens
	tokens, err := m.exchangeCode(provider, result.Code, session.CodeVerifier, session.RedirectPort)
	if err != nil {
		return nil, "", fmt.Errorf("token exchange failed: %w", err)
	}

	// Get user email
	email, err := m.getUserEmail(provider, tokens)
	if err != nil {
		m.log.Warn().Err(err).Msg("Failed to get user email from tokens")
		email = "" // Non-fatal, will need to be provided by user
	}

	m.log.Info().
		Str("provider", session.Provider).
		Str("email", email).
		Msg("OAuth authorization completed successfully")

	return tokens, email, nil
}

// clearSession stops the callback server (if any) and drops the active
// session, whatever it currently is. It is the cleanup path for
// CancelAuthFlow only: a cancel is an explicit user action against
// the flow that is active right now, and it runs atomically under
// mu, so there is no gap in which a new flow could start between
// observing the session and clearing it.
func (m *Manager) clearSession() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callbackServer != nil {
		m.callbackServer.Stop()
		m.callbackServer = nil
	}
	m.activeSession = nil
}

// clearSessionFor stops the captured callback server and drops the
// captured session — but only if they are still the current ones.
// It is the deferred cleanup for WaitForCallback (every exit path),
// and the identity check is what keeps finding 24's guarantee
// intact while fixing re-audit finding R13.
//
// Why it must be identity-checked: the defer runs when the waiter
// returns, which can be AFTER a cancel plus an immediate retry.
// CancelAuthFlow clears the session at once, so startAuthFlowInternal
// (which rejects a new flow only while activeSession != nil) lets the
// retry start while the old waiter is still unwinding — still
// exchanging a code, or just waking from the stopped server. An
// unconditional clear at that point would stop the new flow's
// callback server and nil its session, and the retry would hang or
// fail. The finding-24 invariant is preserved the other way round:
// a new flow can only start once the old session is gone (cancelled,
// or cleared by its own waiter), and this clear can only remove the
// session it was registered for — it can never touch a newer one.
//
// The captured server is stopped even when it is no longer current:
// it is a distinct object from any new flow's server, and Stop is
// idempotent, so stopping it cannot affect the new flow — it only
// guarantees the old port is released on exit paths that did not
// already stop it. The manager fields, by contrast, are only
// touched on a pointer match.
func (m *Manager) clearSessionFor(session *AuthSession, server *CallbackServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callbackServer == server {
		m.callbackServer = nil
	}
	if server != nil {
		server.Stop()
	}
	if m.activeSession == session {
		m.activeSession = nil
	}
}

// CancelAuthFlow cancels any active OAuth flow: whatever session is
// current right now is stopped and dropped, atomically, so a flow
// that starts after this returns is never touched by this call.
// (The deferred cleanup in WaitForCallback is identity-checked
// instead — see clearSessionFor — because that clear runs later,
// after the waiter unwinds, when a retry may already be active.)
func (m *Manager) CancelAuthFlow() {
	m.clearSession()
}

// RefreshToken uses a refresh token to obtain a new access token. Provider is
// resolved by name (back-compat path used by legacy callers). New code paths
// that need a specific client_config_id should call RefreshTokenWithProvider
// with a config obtained via GetProviderForClientConfig.
func (m *Manager) RefreshToken(providerName, refreshToken string) (*TokenResponse, error) {
	provider, err := GetProvider(providerName)
	if err != nil {
		return nil, err
	}
	return m.RefreshTokenWithProvider(provider, refreshToken)
}

// RefreshTokenWithProvider runs the OAuth2 refresh flow against the given
// ProviderConfig. Used by the extension Auth Broker to refresh tokens issued
// under non-mail client configurations (e.g., google-extensions).
func (m *Manager) RefreshTokenWithProvider(provider ProviderConfig, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {provider.ClientID},
	}

	// Add client secret if present (not needed for public clients)
	if provider.ClientSecret != "" {
		data.Set("client_secret", provider.ClientSecret)
	}

	req, err := http.NewRequest("POST", provider.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)
		return nil, fmt.Errorf("token refresh failed: %s - %s", errResp.Error, errResp.ErrorDescription)
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Preserve the refresh token if not returned (some providers don't return it on refresh)
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}

	// A provider that states no lifetime must not produce an
	// already-expired token (fourth-audit finding 36): default it here
	// so every downstream build site sees a sane value.
	if tokens.ExpiresIn <= 0 {
		tokens.ExpiresIn = DefaultTokenLifetimeSeconds
	}

	m.log.Info().
		Str("provider", provider.Name).
		Int("expires_in", tokens.ExpiresIn).
		Msg("Token refreshed successfully")

	return &tokens, nil
}

// HasActiveSession returns true if there's an active OAuth session
func (m *Manager) HasActiveSession() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeSession != nil
}

// exchangeCode exchanges an authorization code for tokens
func (m *Manager) exchangeCode(provider ProviderConfig, code, codeVerifier string, port int) (*TokenResponse, error) {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {provider.ClientID},
		"code_verifier": {codeVerifier},
	}

	// Add client secret if present
	if provider.ClientSecret != "" {
		data.Set("client_secret", provider.ClientSecret)
	}

	req, err := http.NewRequest("POST", provider.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &errResp)
		return nil, fmt.Errorf("token exchange failed (%d): %s - %s", resp.StatusCode, errResp.Error, errResp.ErrorDescription)
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// A provider that states no lifetime must not produce an
	// already-expired token (fourth-audit finding 36).
	if tokens.ExpiresIn <= 0 {
		tokens.ExpiresIn = DefaultTokenLifetimeSeconds
	}

	return &tokens, nil
}

// getUserEmail extracts the user's email from ID token or calls userinfo endpoint
func (m *Manager) getUserEmail(provider ProviderConfig, tokens *TokenResponse) (string, error) {
	// Try to extract from ID token first (if present)
	if tokens.IDToken != "" {
		email, err := extractEmailFromIDToken(tokens.IDToken)
		if err == nil && email != "" {
			return email, nil
		}
	}

	// Fall back to userinfo endpoint. Custom (OIDC-discovered) providers supply their
	// own endpoint; shipped providers use their well-known URLs.
	var userinfoURL string
	switch {
	case provider.UserinfoEndpoint != "":
		userinfoURL = provider.UserinfoEndpoint
	case provider.Name == "google":
		userinfoURL = "https://www.googleapis.com/oauth2/v2/userinfo"
	case provider.Name == "microsoft":
		userinfoURL = "https://graph.microsoft.com/v1.0/me"
	default:
		return "", fmt.Errorf("userinfo not supported for provider: %s", provider.Name)
	}

	req, err := http.NewRequest("GET", userinfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo request failed: %d", resp.StatusCode)
	}

	var userInfo struct {
		Email             string `json:"email"`
		Mail              string `json:"mail"`              // Microsoft uses "mail"
		UserPrincipalName string `json:"userPrincipalName"` // Microsoft fallback
	}

	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return "", err
	}

	// Return first non-empty email
	if userInfo.Email != "" {
		return userInfo.Email, nil
	}
	if userInfo.Mail != "" {
		return userInfo.Mail, nil
	}
	if userInfo.UserPrincipalName != "" {
		return userInfo.UserPrincipalName, nil
	}

	return "", fmt.Errorf("no email found in userinfo response")
}

// buildAuthURL constructs the authorization URL with all required parameters
func buildAuthURL(provider ProviderConfig, state, codeChallenge string, port int) string {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	params := url.Values{
		"client_id":             {provider.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(provider.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	// Provider-specific prompt / refresh-token behavior.
	//
	// Google: needs access_type=offline + prompt=consent on every grant —
	// without prompt=consent, Google won't return a refresh token if the
	// same user has previously consented to this client.
	//
	// Microsoft: refresh tokens come from the `offline_access` scope
	// (already in the provider's Scopes list), NOT from a prompt parameter.
	// access_type is a Google-only parameter; Microsoft ignores it.
	// prompt=consent is actively HARMFUL on Microsoft in tenants with
	// admin-consent policies: it overrides Microsoft's cached admin grant
	// and re-runs the consent decision, which surfaces as the "admin
	// approval required" screen for end users even after the admin has
	// already approved the app. prompt=select_account keeps the account
	// picker behavior (necessary when users have multiple Microsoft
	// sessions cached) without forcing the consent screen.
	switch {
	case strings.HasPrefix(provider.Name, "google"):
		params.Set("access_type", "offline")
		params.Set("prompt", "consent")
	case strings.HasPrefix(provider.Name, "microsoft"):
		params.Set("prompt", "select_account")
	}
	if provider.LoginHint != "" {
		// Pre-fill the account picker — Google and Microsoft both honor
		// this. Empty = the user picks an account fresh.
		params.Set("login_hint", provider.LoginHint)
	}

	authURL := provider.AuthURL + "?" + params.Encode()
	return authURL
}

// generateCodeVerifier creates a cryptographically random code verifier for PKCE
// Per RFC 7636, verifier should be 43-128 characters from [A-Z] / [a-z] / [0-9] / "-" / "." / "_" / "~"
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32) // Will be 43 chars when base64url encoded
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge creates the S256 code challenge from the verifier
func generateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateState creates a cryptographically random state parameter for CSRF protection
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeIDTokenClaims decodes a JWT ID token's payload into out. No signature
// verification — we trust the token endpoint we just exchanged with over TLS.
func decodeIDTokenClaims(idToken string, out interface{}) error {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid ID token format")
	}
	// Decode payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Try standard base64 with padding
		payload, err = base64.StdEncoding.DecodeString(parts[1] + "==")
		if err != nil {
			return fmt.Errorf("failed to decode ID token payload: %w", err)
		}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("failed to parse ID token claims: %w", err)
	}
	return nil
}

// extractEmailFromIDToken extracts the email claim from a JWT ID token.
func extractEmailFromIDToken(idToken string) (string, error) {
	var claims struct {
		Email string `json:"email"`
	}
	if err := decodeIDTokenClaims(idToken, &claims); err != nil {
		return "", err
	}
	return claims.Email, nil
}

// ExtractStableSubjectFromIDToken returns a stable, immutable account identity
// from the ID token for incremental-consent validation. Microsoft ID tokens
// carry `oid` (object ID) + `tid` (tenant ID) which — unlike email/UPN/primary
// SMTP — never change and are identical across the mail and calendar/contacts
// OAuth clients of the same account. Returns "<tid>:<oid>" when both are present,
// otherwise "" (Google and others fall back to the email compare, whose email
// claim is stable). Never fails: an empty/unparseable token yields "".
func ExtractStableSubjectFromIDToken(idToken string) string {
	if idToken == "" {
		return ""
	}
	var claims struct {
		OID string `json:"oid"`
		TID string `json:"tid"`
	}
	if err := decodeIDTokenClaims(idToken, &claims); err != nil {
		return ""
	}
	if claims.OID == "" || claims.TID == "" {
		return ""
	}
	return claims.TID + ":" + claims.OID
}
