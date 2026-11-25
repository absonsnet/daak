package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/certificate"
	"github.com/absonsnet/daak/internal/contact"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/draft"
	"github.com/absonsnet/daak/internal/email"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/imap"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/message"
	"github.com/absonsnet/daak/internal/oauth2"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/absonsnet/daak/internal/sendqueue"
	"github.com/absonsnet/daak/internal/settings"
	"github.com/absonsnet/daak/internal/smime"
	"github.com/absonsnet/daak/internal/smtp"
	goImap "github.com/emersion/go-imap/v2"
	"github.com/rs/zerolog"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ComposerAttachment represents an attachment in the compose window
type ComposerAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int    `json:"size"`
	Data        string `json:"data"` // Base64 encoded
}

// composeOps holds shared dependencies for compose-related operations
// used by both App and ComposerApp.
type composeOps struct {
	accountStore   *account.Store
	folderStore    *folder.Store
	credStore      *credentials.Store
	certStore      *certificate.Store
	contactStore   *contact.Store
	oauth2Manager  *oauth2.Manager
	smimeStore     *smime.Store
	smimeSigner    *smime.Signer
	smimeEncryptor *smime.Encryptor
	pgpStore       *pgp.Store
	pgpSigner      *pgp.Signer
	pgpEncryptor   *pgp.Encryptor
	draftOps       *draftOps // for draft cleanup on send

	// rawSendOverride, when non-nil, replaces the SMTP dial/send in
	// sendRawMessage. sentSaveOverride, when non-nil, replaces the IMAP
	// APPEND in saveToSentFolder. Both are nil in production; tests set
	// them to capture outbound bytes instead of reaching a server.
	rawSendOverride  func(ctx context.Context, from string, recipients []string, raw []byte) error
	sentSaveOverride func(ctx context.Context, accountID string, raw []byte) error

	// saveRefreshedTokensOverride, when non-nil, replaces the credential
	// store write in getValidOAuthToken. Nil in production; tests set it
	// to inject a token-save failure (audit finding R12).
	saveRefreshedTokensOverride func(accountID string, tokens *credentials.OAuthTokens) error

	// saveRecipientsToAccountContacts support (nil in the detached
	// composer/viewer, whose sends go through the main process queue —
	// the provider save silently skips when any of these is nil).
	settingsStore           *settings.Store
	carddavStore            *carddav.Store
	carddavSyncer           *carddav.Syncer
	googleContactsClient    *contact.GoogleContactsClient
	microsoftContactsSyncer *contact.MicrosoftContactsSyncer
	getContactSourceToken   func(sourceID string) (string, error)

	// onCardDAVAutoAddFailure, when non-nil, is called (once per send
	// batch) when one or more recipients could not be saved to any
	// address book of the sending account's CardDAV source (R22).
	// Production wires it to a user-visible warning (Wails event +
	// desktop notification); tests set it to capture the failure.
	// Nil means log-only — the detached composer/viewer leave it nil
	// because their sends go through the main process queue, where
	// the save (and the warning) happens.
	onCardDAVAutoAddFailure func(sourceID, accountID string, emails []string, err error)

	// msContactsSourceCache maps mail account IDs to their Microsoft
	// contacts source IDs (see microsoftContactsSourceForAccount).
	msContactsSourceCache   map[string]string
	msContactsSourceCacheMu sync.Mutex

	// oauthRefreshLocksMu guards oauthRefreshLocks. oauthRefreshLocks
	// serialises token refreshes per account (audit finding 23):
	// getValidOAuthToken is called concurrently from IMAP sync, the
	// send-queue worker, CardDAV and contacts auto-add, and with rotating
	// refresh tokens two concurrent refreshes can make the second fail
	// invalid_grant even though a valid token was just stored. Holding
	// the per-account lock across the refresh and re-reading the stored
	// tokens after acquiring it means only one refresh runs per account
	// and every other caller reuses the fresh tokens.
	oauthRefreshLocksMu sync.Mutex
	oauthRefreshLocks   map[string]*sync.Mutex
}

// oauthRefreshLock returns the refresh mutex for an account, creating it
// on first use.
func (ops *composeOps) oauthRefreshLock(accountID string) *sync.Mutex {
	ops.oauthRefreshLocksMu.Lock()
	defer ops.oauthRefreshLocksMu.Unlock()
	if ops.oauthRefreshLocks == nil {
		ops.oauthRefreshLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := ops.oauthRefreshLocks[accountID]
	if !ok {
		mu = &sync.Mutex{}
		ops.oauthRefreshLocks[accountID] = mu
	}
	return mu
}

// saveRefreshedTokensWithRetry persists freshly refreshed tokens, retrying
// transient keyring/database failures (audit finding R12). A refresh
// rotates the refresh token on the provider, so the old stored refresh
// token is already revoked by the time the save runs — a swallowed save
// failure leaves the revoked token in storage and the next refresh fails
// invalid_grant, prompting a spurious re-sign-in. Returns the last error
// after 3 attempts; callers must treat that as a failed refresh, not
// continue on the in-memory tokens.
func saveRefreshedTokensWithRetry(save func() error) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
		}
		if err = save(); err == nil {
			return nil
		}
	}
	return err
}

// emitOAuthEvent emits a Wails event only when ctx is a Wails runtime
// context (one carrying the events bus). Plain contexts (unit tests,
// background callers) have no bus, and wailsRuntime.EventsEmit fatal-exits
// on them, so the emit is skipped there — the returned error still
// surfaces the failure to the caller.
func emitOAuthEvent(ctx context.Context, name string, data map[string]interface{}) {
	if ctx == nil || ctx.Value("events") == nil {
		return
	}
	wailsRuntime.EventsEmit(ctx, name, data)
}

// getValidOAuthToken returns a valid OAuth token, refreshing if needed.
// ctx is the caller's Wails context (for EventsEmit on reauth).
func (ops *composeOps) getValidOAuthToken(ctx context.Context, accountID string) (*credentials.OAuthTokens, error) {
	log := logging.WithComponent("composeOps")

	tokens, err := ops.credStore.GetOAuthTokens(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth tokens: %w", err)
	}

	// Check if token expires within 5 minutes
	if !tokens.IsExpiringSoon(5 * time.Minute) {
		return tokens, nil
	}

	// Serialize refreshes for this account: without the lock, two
	// concurrent callers both read the same expiring tokens and both
	// refresh, and with rotating refresh tokens the second refresh fails
	// invalid_grant on the already-rotated token. Other accounts refresh
	// independently — the lock is per account, not global.
	mu := ops.oauthRefreshLock(accountID)
	mu.Lock()
	defer mu.Unlock()

	// Re-read after acquiring the lock: a caller that held the lock
	// before us may have just refreshed and stored fresh tokens, in
	// which case there is nothing left to refresh.
	tokens, err = ops.credStore.GetOAuthTokens(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth tokens: %w", err)
	}
	if !tokens.IsExpiringSoon(5 * time.Minute) {
		return tokens, nil
	}

	log.Info().
		Str("account_id", accountID).
		Time("expires_at", tokens.ExpiresAt).
		Msg("OAuth token expiring soon, refreshing")

	// Refresh the token
	newTokenResp, err := ops.refreshOAuthToken(accountID, tokens)
	if err != nil {
		log.Error().Err(err).
			Str("account_id", accountID).
			Msg("OAuth token refresh failed")

		// Emit event for frontend to prompt re-authorization
		emitOAuthEvent(ctx, "oauth:reauth-required", map[string]interface{}{
			"accountId": accountID,
			"provider":  tokens.Provider,
			"error":     err.Error(),
		})

		return nil, fmt.Errorf("OAuth token refresh failed, re-authorization required: %w", err)
	}

	// Calculate new expiry time
	expiresAt := time.Now().Add(time.Duration(newTokenResp.ExpiresIn) * time.Second)

	// Update tokens in store
	tokens.AccessToken = newTokenResp.AccessToken
	tokens.ExpiresAt = expiresAt
	if newTokenResp.RefreshToken != "" {
		tokens.RefreshToken = newTokenResp.RefreshToken
	}

	// Persist the refreshed tokens (audit finding R12). The provider has
	// already rotated the refresh token, so a failed save must NOT be
	// swallowed: continuing on the in-memory tokens would leave the
	// revoked refresh token in storage and the next refresh would fail
	// invalid_grant. Retry transient failures, then fail loudly with a
	// distinct event — deliberately NOT oauth:reauth-required, because
	// the grant is fine and the user must not be misled into
	// re-authorizing; the fix is to retry, and re-authorize only if
	// sign-in is actually requested again.
	saveTokens := func() error { return ops.credStore.SetOAuthTokens(accountID, tokens) }
	if ops.saveRefreshedTokensOverride != nil {
		saveTokens = func() error { return ops.saveRefreshedTokensOverride(accountID, tokens) }
	}
	if err := saveRefreshedTokensWithRetry(saveTokens); err != nil {
		log.Error().Err(err).
			Str("account_id", accountID).
			Msg("Failed to save refreshed OAuth tokens after retries")
		emitOAuthEvent(ctx, "oauth:tokens-save-failed", map[string]interface{}{
			"accountId": accountID,
			"provider":  tokens.Provider,
			"error":     err.Error(),
		})
		return nil, fmt.Errorf("OAuth tokens refreshed but could not be saved: %w (sign-in is still valid — retry, and re-authorize only if asked)", err)
	}

	log.Info().
		Str("account_id", accountID).
		Time("new_expires_at", expiresAt).
		Msg("OAuth token refreshed successfully")

	return tokens, nil
}

// refreshOAuthToken obtains a new access token using the stored refresh token,
// resolving the provider config by name for shipped providers (Google/Microsoft) or
// from per-account storage for custom ("bring your own app") providers — whose
// endpoints/creds oauth2.GetProvider can't supply. The default branch is unchanged from
// the prior inline call, so shipped accounts behave exactly as before.
func (ops *composeOps) refreshOAuthToken(accountID string, tokens *credentials.OAuthTokens) (*oauth2.TokenResponse, error) {
	if tokens.Provider != customOAuthProviderName {
		return ops.oauth2Manager.RefreshToken(tokens.Provider, tokens.RefreshToken)
	}

	cfg, ok, err := ops.credStore.GetCustomOAuthProvider(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to load custom OAuth provider: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("custom OAuth provider config missing for account")
	}

	provider := oauth2.CustomProviderConfig(cfg.AuthURL, cfg.TokenURL, cfg.UserinfoEndpoint, cfg.Scopes, cfg.ClientID, cfg.ClientSecret)
	return ops.oauth2Manager.RefreshTokenWithProvider(provider, tokens.RefreshToken)
}

// getIMAPCredentials returns IMAP credentials for an account.
// Handles both password and OAuth2 authentication.
func (ops *composeOps) getIMAPCredentials(ctx context.Context, accountID string) (*imap.ClientConfig, error) {
	acc, err := ops.accountStore.Get(accountID)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", accountID)
	}

	config := imap.DefaultConfig()
	config.Host = acc.IMAPHost
	config.Port = acc.IMAPPort
	config.Security = imap.SecurityType(acc.IMAPSecurity)
	config.AuthMechanism = string(acc.IMAPAuthMechanism)
	config.Username = acc.Username
	config.TLSConfig = certificate.BuildTLSConfig(acc.IMAPHost, ops.certStore)
	// Identify only to servers that have demanded it (learned flag, #457).
	config.SendID = acc.IMAPRequireID
	config.ClientName = platform.AppName
	config.ClientVersion = Version
	config.ClientVendor = "github.com/absonsnet/daak"

	switch acc.AuthType {
	case account.AuthOAuth2:
		tokens, err := ops.getValidOAuthToken(ctx, accountID)
		if err != nil {
			return nil, fmt.Errorf("failed to get OAuth token: %w", err)
		}
		config.AuthType = imap.AuthTypeOAuth2
		config.AccessToken = tokens.AccessToken
	default:
		password, err := ops.credStore.GetPassword(accountID)
		if err != nil {
			return nil, fmt.Errorf("failed to get password: %w", err)
		}
		config.AuthType = imap.AuthTypePassword
		config.Password = password
	}

	return &config, nil
}

// getSpecialFolder resolves a special folder for an account, checking the
// user's explicit mapping first, then falling back to auto-detected type.
// Mirrors App.GetSpecialFolder (app/folder.go) for use within composeOps.
func (ops *composeOps) getSpecialFolder(accountID string, folderType folder.Type) (*folder.Folder, error) {
	acc, err := ops.accountStore.Get(accountID)
	if err != nil || acc == nil {
		return ops.folderStore.GetByType(accountID, folderType)
	}
	mappedPath := acc.GetFolderMapping(string(folderType))
	if mappedPath != "" {
		f, err := ops.folderStore.GetByPath(accountID, mappedPath)
		if err == nil && f != nil {
			return f, nil
		}
	}
	return ops.folderStore.GetByType(accountID, folderType)
}

// saveToSentFolder appends the sent message to the Sent folder via IMAP.
func (ops *composeOps) saveToSentFolder(ctx context.Context, accountID string, acc *account.Account, rawMsg []byte) error {
	if ops.sentSaveOverride != nil {
		return ops.sentSaveOverride(ctx, accountID, rawMsg)
	}
	log := logging.WithComponent("composeOps")

	// Resolve the Sent folder using the same logic as App.GetSpecialFolder
	sentFolder, err := ops.getSpecialFolder(accountID, folder.TypeSent)
	if err != nil || sentFolder == nil {
		return fmt.Errorf("no Sent folder configured or detected")
	}
	sentPath := sentFolder.Path

	log.Info().
		Str("account_id", accountID).
		Str("sent_path", sentPath).
		Msg("Saving sent message to folder via IMAP APPEND")

	// Create IMAP client
	clientConfig, err := ops.getIMAPCredentials(ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to get IMAP credentials: %w", err)
	}

	imapClient := imap.NewClient(*clientConfig)
	if err := imapClient.Connect(); err != nil {
		return fmt.Errorf("failed to connect to IMAP: %w", err)
	}
	defer imapClient.Close()

	if err := imapClient.Login(); err != nil {
		return fmt.Errorf("failed to login to IMAP: %w", err)
	}

	// Append message with \Seen flag
	flags := []goImap.Flag{goImap.FlagSeen}
	_, err = imapClient.AppendMessage(sentPath, flags, time.Now(), rawMsg)
	if err != nil {
		return fmt.Errorf("failed to append to Sent folder: %w", err)
	}

	log.Info().
		Str("account_id", accountID).
		Str("sent_path", sentPath).
		Msg("Message saved to Sent folder")

	return nil
}

// hasSMIMECertificate returns whether the account has a valid default S/MIME certificate.
func (ops *composeOps) hasSMIMECertificate(accountID string) bool {
	cert, _, err := ops.smimeStore.GetDefaultCertificate(accountID)
	return err == nil && cert != nil && !cert.IsExpired
}

// hasPGPKey returns whether the account has a valid default PGP key.
func (ops *composeOps) hasPGPKey(accountID string) bool {
	key, _, err := ops.pgpStore.GetDefaultKey(accountID)
	return err == nil && key != nil && !key.IsExpired
}

// shouldSignMessage determines whether a message should be S/MIME signed.
func (ops *composeOps) shouldSignMessage(accountID string, perMessageOverride bool) bool {
	if perMessageOverride {
		return ops.hasSMIMECertificate(accountID)
	}
	policy, err := ops.smimeStore.GetSignPolicy(accountID)
	if err != nil || policy != "always" {
		return false
	}
	return ops.hasSMIMECertificate(accountID)
}

// shouldEncryptMessage determines whether a message should be S/MIME encrypted.
func (ops *composeOps) shouldEncryptMessage(accountID string, perMessageOverride bool) bool {
	if perMessageOverride {
		return ops.hasSMIMECertificate(accountID)
	}
	policy, err := ops.smimeStore.GetEncryptPolicy(accountID)
	if err != nil || policy != "always" {
		return false
	}
	return ops.hasSMIMECertificate(accountID)
}

// shouldPGPSignMessage determines whether a message should be PGP signed.
func (ops *composeOps) shouldPGPSignMessage(accountID string, perMessageOverride bool) bool {
	if perMessageOverride {
		return ops.hasPGPKey(accountID)
	}
	policy, err := ops.pgpStore.GetSignPolicy(accountID)
	if err != nil || policy != "always" {
		return false
	}
	return ops.hasPGPKey(accountID)
}

// shouldPGPEncryptMessage determines whether a message should be PGP encrypted.
func (ops *composeOps) shouldPGPEncryptMessage(accountID string, perMessageOverride bool) bool {
	if perMessageOverride {
		return ops.hasPGPKey(accountID)
	}
	policy, err := ops.pgpStore.GetEncryptPolicy(accountID)
	if err != nil || policy != "always" {
		return false
	}
	return ops.hasPGPKey(accountID)
}

// smtpClientConfig builds the SMTP client config for an account: host,
// security, username (shared-mailbox parent / separate SMTP credentials
// overrides), TLS, and password vs OAuth2 authentication. Extracted
// verbatim from sendMessage so Mail Merge's reused-connection sender
// (app/extension_mail_merge.go) authenticates exactly like a normal
// send. acc.ID is the account id (== the sendMessage accountID).
func (ops *composeOps) smtpClientConfig(ctx context.Context, acc *account.Account) (smtp.ClientConfig, error) {
	accountID := acc.ID
	smtpConfig := smtp.DefaultConfig()
	smtpConfig.Host = acc.SMTPHost
	smtpConfig.Port = acc.SMTPPort
	smtpConfig.Security = smtp.SecurityType(acc.SMTPSecurity)
	smtpConfig.AuthMechanism = string(acc.EffectiveSMTPAuthMechanism())
	smtpConfig.Username = acc.Username
	// Shared mailboxes authenticate SMTP with the parent account's username
	if acc.SharedMailboxParentID != "" {
		parent, parentErr := ops.accountStore.Get(acc.SharedMailboxParentID)
		if parentErr == nil && parent != nil {
			smtpConfig.Username = parent.Username
		}
	}
	// Separate SMTP credentials override (Generic provider; gated by
	// non-empty SMTPUsername at the model layer). Doesn't apply to OAuth
	// accounts — those keep the bearer-token path below.
	smtpUsesSeparateCreds := acc.SMTPUsername != "" && acc.AuthType != account.AuthOAuth2
	if smtpUsesSeparateCreds {
		smtpConfig.Username = acc.SMTPUsername
	}
	smtpConfig.TLSConfig = certificate.BuildTLSConfig(acc.SMTPHost, ops.certStore)

	// Handle authentication based on auth type
	switch acc.AuthType {
	case account.AuthOAuth2:
		tokens, tokenErr := ops.getValidOAuthToken(ctx, accountID)
		if tokenErr != nil {
			return smtp.ClientConfig{}, fmt.Errorf("failed to get OAuth token: %w", tokenErr)
		}
		smtpConfig.AuthType = smtp.AuthTypeOAuth2
		smtpConfig.AccessToken = tokens.AccessToken
	default:
		smtpConfig.AuthType = smtp.AuthTypePassword
		if smtpUsesSeparateCreds {
			password, passErr := ops.credStore.GetSMTPPassword(accountID)
			if passErr != nil {
				return smtp.ClientConfig{}, fmt.Errorf("failed to get SMTP password: %w", passErr)
			}
			smtpConfig.Password = password
			break
		}
		password, passErr := ops.credStore.GetPassword(accountID)
		if passErr != nil {
			return smtp.ClientConfig{}, fmt.Errorf("failed to get password: %w", passErr)
		}
		smtpConfig.Password = password
	}
	return smtpConfig, nil
}

// sendRawMessage sends already-built RFC 822 bytes through the account's
// SMTP server, reusing the same client config (auth, TLS, credentials)
// as a normal send. Callers that build their own MIME (e.g. reactions,
// whose multipart structure ComposeMessage cannot express) use this
// instead of sendMessage. The receive-only check lives here so every
// raw sender gets it.
func (ops *composeOps) sendRawMessage(ctx context.Context, accountID, from string, recipients []string, raw []byte) error {
	if ops.rawSendOverride != nil {
		return ops.rawSendOverride(ctx, from, recipients, raw)
	}
	if len(recipients) == 0 {
		return fmt.Errorf("no recipients specified")
	}

	acc, err := ops.accountStore.Get(accountID)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}
	if acc == nil {
		return fmt.Errorf("account not found: %s", accountID)
	}
	if acc.NoOutgoingServer {
		return fmt.Errorf("account %q is configured as receive-only (no outgoing server)", acc.Email)
	}

	smtpConfig, err := ops.smtpClientConfig(ctx, acc)
	if err != nil {
		return err
	}

	client := smtp.NewClient(smtpConfig)
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	defer client.Close()

	if err := client.Login(); err != nil {
		return fmt.Errorf("failed to login to SMTP server: %w", err)
	}

	if err := client.SendMail(from, recipients, raw); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// sendMessage performs the full send flow: build RFC822, sign, encrypt, SMTP send,
// save to Sent folder, add recipients to contacts, and optionally delete a draft.
// When d is non-nil, the draft is fully cleaned up (IMAP + message row + DB) after
// a successful send. Returns the account for callers that need it post-send.
//
// sendMessage is sendMessageWithHook with no hook; see that method for
// the hook's contract.
//
// The third return is the Sent-folder save error, if any (re-audit
// finding R24): the mail was already accepted by SMTP when the save
// runs, so a save failure never fails the send — but it must not be
// swallowed either, or a delivered mail vanishes from Daak with no
// trace. Callers record it as a persistent "Sent save failed"
// warning (see sendQueueService.recordSentSaveFailure). It is nil
// when the provider auto-saves Sent mail (no save attempted) or the
// save succeeded.
func (ops *composeOps) sendMessage(ctx context.Context, accountID string, msg smtp.ComposeMessage, d *draft.Draft) (*account.Account, error, error) {
	return ops.sendMessageWithHook(ctx, accountID, msg, d, nil)
}

// sendMessageWithHook is sendMessage with an afterSMTP hook. The hook
// runs immediately after the SMTP transaction succeeds — before the
// Sent-folder save, contact updates and draft cleanup — and receives
// the exact bytes that were transmitted. It must never fail the send:
// the mail is already out, so the hook has no error return; a hook
// that panics would lose the post-send bookkeeping, so callers keep
// it to a short durable write.
//
// The send-queue worker uses the hook to delete (or mark sent) the
// queue row the moment SMTP accepts the mail. Before this, the row
// was only deleted after sendMessage returned, so a crash during the
// (slow, network) Sent-folder save left a 'sending' row that the
// stale-claim reset re-queued and re-sent. The hook shrinks that
// window to the SMTP return itself; combined with the stable
// Message-ID assigned at queue time, a redispatch after a crash
// carries the same identity as the first attempt.
//
// The third return is the Sent-folder save error (see sendMessage):
// non-nil only when SMTP already accepted the mail and the save then
// failed. Callers must surface it as a persistent warning, never
// drop it — the queue row is already gone by then, so without the
// warning the delivered mail has no record anywhere in Daak.
func (ops *composeOps) sendMessageWithHook(ctx context.Context, accountID string, msg smtp.ComposeMessage, d *draft.Draft, afterSMTP func(raw []byte)) (*account.Account, error, error) {
	log := logging.WithComponent("composeOps")

	log.Info().
		Str("accountID", accountID).
		Str("from", msg.From.Address).
		Int("toCount", len(msg.To)).
		Str("subject", msg.Subject).
		Msg("Sending message")

	// Get account
	acc, err := ops.accountStore.Get(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err), nil
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", accountID), nil
	}

	// Receive-only accounts have no SMTP wiring; reject sends before any
	// further work. The composer's From dropdown already filters these
	// out, so reaching this branch indicates a stale draft or a programmatic
	// caller — surface a clear error rather than dialing a non-existent
	// SMTP server.
	if acc.NoOutgoingServer {
		return nil, fmt.Errorf("account %q is configured as receive-only (no outgoing server)", acc.Email), nil
	}

	// Build RFC822 message
	rawMsg, err := msg.ToRFC822()
	if err != nil {
		return nil, fmt.Errorf("failed to build message: %w", err), nil
	}

	fromEmail := msg.From.Address

	// S/MIME signing (if configured for this account/message)
	if ops.shouldSignMessage(accountID, msg.SignMessage) {
		signedMsg, signErr := ops.smimeSigner.SignMessage(accountID, fromEmail, rawMsg)
		if signErr != nil {
			return nil, fmt.Errorf("failed to sign message: %w", signErr), nil
		}
		rawMsg = signedMsg
		log.Info().Str("accountID", accountID).Msg("Message signed with S/MIME")
	}

	// S/MIME encryption (if configured) — sign-then-encrypt per RFC 5751
	if ops.shouldEncryptMessage(accountID, msg.EncryptMessage) {
		encryptedMsg, encErr := ops.smimeEncryptor.EncryptMessage(accountID, fromEmail, msg.AllRecipients(), rawMsg)
		if encErr != nil {
			return nil, fmt.Errorf("failed to encrypt message: %w", encErr), nil
		}
		rawMsg = encryptedMsg
		log.Info().Str("accountID", accountID).Msg("Message encrypted with S/MIME")
	}

	// PGP signing (mutually exclusive with S/MIME — only if S/MIME sign was not applied)
	if !msg.SignMessage && ops.shouldPGPSignMessage(accountID, msg.PGPSignMessage) {
		signedMsg, signErr := ops.pgpSigner.SignMessage(accountID, fromEmail, rawMsg)
		if signErr != nil {
			return nil, fmt.Errorf("failed to PGP sign message: %w", signErr), nil
		}
		rawMsg = signedMsg
		log.Info().Str("accountID", accountID).Msg("Message signed with PGP")
	}

	// PGP encryption (mutually exclusive with S/MIME — only if S/MIME encrypt was not applied)
	if !msg.EncryptMessage && ops.shouldPGPEncryptMessage(accountID, msg.PGPEncryptMessage) {
		encryptedMsg, encErr := ops.pgpEncryptor.EncryptMessage(accountID, fromEmail, msg.AllRecipients(), rawMsg)
		if encErr != nil {
			return nil, fmt.Errorf("failed to PGP encrypt message: %w", encErr), nil
		}
		rawMsg = encryptedMsg
		log.Info().Str("accountID", accountID).Msg("Message encrypted with PGP")
	}

	recipients := msg.AllRecipients()
	if len(recipients) == 0 {
		return nil, fmt.Errorf("no recipients specified"), nil
	}

	if err := ops.sendRawMessage(ctx, accountID, msg.From.Address, recipients, rawMsg); err != nil {
		return nil, err, nil
	}

	// SMTP accepted the mail: record that durably before any
	// post-send work (Sent save, contacts, draft cleanup). See
	// sendMessageWithHook's doc comment.
	if afterSMTP != nil {
		afterSMTP(rawMsg)
	}

	// Save to Sent folder (using IMAP APPEND) if provider doesn't auto-save.
	// A failure here must not fail the send — SMTP already accepted
	// the mail — but it is returned (re-audit finding R24) so the
	// caller can record a persistent "Sent save failed" warning:
	// the queue row is already gone (afterSMTP above deleted it),
	// so a swallowed error would leave a delivered mail with no
	// record anywhere in Daak.
	var sentSaveErr error
	if !providerAutoSavesSentMail(acc.IMAPHost) {
		log.Info().Str("host", acc.IMAPHost).Msg("Provider doesn't auto-save, using IMAP APPEND")
		if err := ops.saveToSentFolder(ctx, accountID, acc, rawMsg); err != nil {
			log.Warn().Err(err).Msg("Failed to save message to Sent folder")
			sentSaveErr = err
		}
	}

	// Add recipients to local contacts (best-effort; send already succeeded)
	for _, to := range msg.To {
		_ = ops.contactStore.AddOrUpdate(to.Address, to.Name)
	}
	for _, cc := range msg.Cc {
		_ = ops.contactStore.AddOrUpdate(cc.Address, cc.Name)
	}

	// Save new recipients to the sending account's own contacts (Google
	// Contacts, Microsoft contacts, or the account's CardDAV address
	// book) — best-effort on a worker goroutine. sendMessage runs on the
	// foreground Wails call for direct sends, so the provider calls must
	// never delay it; the send already succeeded either way.
	go func() {
		defer recoverPanic("app.compose", "save recipients to account contacts")
		ops.saveRecipientsToAccountContacts(ctx, accountID, msg)
	}()

	// Delete draft if one was provided (send already succeeded — log errors, don't fail)
	if d != nil && ops.draftOps != nil {
		if _, delErr := ops.draftOps.deleteDraftCore(ctx, d); delErr != nil {
			log.Warn().Err(delErr).Str("draftID", d.ID).Msg("Failed to delete draft after send")
		}
	}

	log.Info().Str("accountID", accountID).Msg("Message sent successfully")
	return acc, nil, sentSaveErr
}

// readFileAsAttachment reads a file and creates a ComposerAttachment.
func readFileAsAttachment(filePath string) (*ComposerAttachment, error) {
	log := logging.WithComponent("compose")

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	filename := filepath.Base(filePath)
	contentType := detectContentType(filename)
	encoded := base64.StdEncoding.EncodeToString(content)

	log.Debug().
		Str("filename", filename).
		Str("contentType", contentType).
		Int("size", len(content)).
		Msg("File read as attachment")

	return &ComposerAttachment{
		Filename:    filename,
		ContentType: contentType,
		Size:        len(content),
		Data:        encoded,
	}, nil
}

// pickAttachmentFiles opens a file picker dialog and returns the selected files as attachments.
func pickAttachmentFiles(ctx context.Context) ([]ComposerAttachment, error) {
	log := logging.WithComponent("compose")

	files, err := wailsRuntime.OpenMultipleFilesDialog(ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Attachments",
	})
	if err != nil {
		log.Error().Err(err).Msg("Failed to show file picker dialog")
		return nil, fmt.Errorf("failed to show file picker: %w", err)
	}

	if len(files) == 0 {
		return nil, nil
	}

	var attachments []ComposerAttachment
	for _, filePath := range files {
		att, err := readFileAsAttachment(filePath)
		if err != nil {
			log.Warn().Err(err).Str("path", filePath).Msg("Failed to read file as attachment")
			continue
		}
		attachments = append(attachments, *att)
	}

	log.Info().Int("count", len(attachments)).Msg("Files picked for attachment")
	return attachments, nil
}

// ============================================================================
// Compose API - Exposed to frontend via Wails bindings
// ============================================================================

// SendMessage sends an email via SMTP.
// The message is composed in the frontend and sent to the backend.
// A Sent-folder save failure does not fail the send (the mail was
// already accepted by SMTP), but it is recorded as a persistent
// "Sent save failed" Outbox warning (re-audit finding R24) instead
// of vanishing with only a log line.
func (a *App) SendMessage(accountID string, msg smtp.ComposeMessage) error {
	_, err, sentSaveErr := a.composeOps.sendMessage(a.ctx, accountID, msg, nil)
	if err != nil {
		return err
	}
	if sentSaveErr != nil {
		a.ensureSendQueueService().recordSentSaveFailure(sendqueue.Item{
			AccountID:   accountID,
			Kind:        sendqueue.KindMessage,
			Subject:     msg.Subject,
			Recipient:   firstRecipient(msg),
			FromAddress: msg.From.Address,
			To:          msg.AllRecipients(),
		}, time.Now(), sentSaveErr)
		return nil
	}
	go func() {
		if err := a.syncSentFolder(accountID); err != nil {
			log := logging.WithComponent("app")
			log.Debug().Err(err).Str("accountID", accountID).Msg("Sent folder sync failed")
		}
	}()
	return nil
}

// firstRecipient returns the first To address of a composed message,
// "" when there is none. Used for the "Sent save failed" Outbox
// warning, which names the message by subject and recipient.
func firstRecipient(msg smtp.ComposeMessage) string {
	if len(msg.To) > 0 {
		return msg.To[0].Address
	}
	return ""
}

// handleExternalMailto handles a mailto URL received from a second instance.
// Routes to inline or detached composer based on the mailto_mode setting.
func (a *App) handleExternalMailto(rawURL string) {
	log := logging.WithComponent("app")

	mailtoData := ParseMailtoURL(rawURL)
	if mailtoData == nil {
		log.Warn().Str("url", rawURL).Msg("Invalid mailto URL from second instance")
		return
	}

	mailtoMode, _ := a.settingsStore.GetMailtoMode()
	log.Info().Str("mode", mailtoMode).Msg("Handling external mailto")

	if mailtoMode == "detached" {
		// Pick first account for detached composer
		accounts, err := a.accountStore.List()
		if err != nil || len(accounts) == 0 {
			log.Warn().Msg("No accounts available for mailto")
			return
		}
		if err := a.OpenComposerWindow(accounts[0].ID, "new", "", "", rawURL); err != nil {
			log.Warn().Err(err).Msg("Failed to open detached composer for mailto")
		}
		return
	}

	// Inline mode: show window and emit event for frontend
	a.ShowWindow()
	wailsRuntime.EventsEmit(a.ctx, "mailto:external", mailtoData)
}

// syncSentFolder syncs the Sent folder for an account after sending a message
func (a *App) syncSentFolder(accountID string) error {
	defer recoverPanic("app.compose", "sync sent folder")
	log := logging.WithComponent("app")

	sentFolder, err := a.GetSpecialFolder(accountID, folder.TypeSent)
	if err != nil || sentFolder == nil {
		log.Warn().Str("accountID", accountID).Msg("Could not find Sent folder for sync")
		return nil
	}

	// Get account to determine sync period
	acc, _ := a.accountStore.Get(accountID)
	syncPeriodDays := 30 // default
	if acc != nil {
		syncPeriodDays = acc.SyncPeriodDays
	}

	// Sync progress feedback rides on the syncEngine's sync:progress callback (see app.go:438)
	if err := a.syncEngine.SyncMessages(a.ctx, accountID, sentFolder.ID, syncPeriodDays, false); err != nil {
		log.Warn().Err(err).Str("folderID", sentFolder.ID).Msg("Failed to sync Sent folder")
	}

	// Also fetch bodies
	if err := a.syncEngine.FetchBodiesInBackground(a.ctx, accountID, sentFolder.ID, syncPeriodDays); err != nil {
		log.Warn().Err(err).Str("folderID", sentFolder.ID).Msg("Failed to fetch bodies for Sent folder")
	}

	// Emit synced event
	wailsRuntime.EventsEmit(a.ctx, "folder:synced", map[string]interface{}{
		"accountId": accountID,
		"folderId":  sentFolder.ID,
	})

	// Notify conversation viewer that sent folder synced (for cross-folder thread refresh)
	wailsRuntime.EventsEmit(a.ctx, "sent:synced", map[string]interface{}{
		"accountId": accountID,
	})

	return nil
}

// PrepareReply prepares a reply message structure from an existing message.
// mode can be "reply", "reply-all", or "forward"
func (a *App) PrepareReply(messageID, mode string) (*smtp.ComposeMessage, error) {
	log := logging.WithComponent("app")
	log.Debug().Str("messageID", messageID).Str("mode", mode).Msg("Preparing reply message")

	// Get the original message
	msg, err := a.messageStore.Get(messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	if msg == nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}

	// Get account and identities
	identities, err := a.accountStore.GetIdentities(msg.AccountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get identities: %w", err)
	}

	// Prefer the identity the original message was addressed to (To/Cc/Bcc), so the
	// reply goes out from the alias that received the mail (#325); then the default
	// identity; then the first.
	fromIdentity := selectReplyFromIdentity(identities, msg)
	if fromIdentity == nil {
		acc, _ := a.accountStore.Get(msg.AccountID)
		if acc != nil {
			fromIdentity = &account.Identity{
				Email: acc.Email,
				Name:  acc.Name,
			}
		}
	}

	// Build the From address
	from := smtp.Address{}
	if fromIdentity != nil {
		from = smtp.Address{Name: fromIdentity.Name, Address: fromIdentity.Email}
	}

	// Build subject with Re: or Fwd: prefix
	subject := msg.Subject
	switch mode {
	case "forward":
		if !strings.HasPrefix(strings.ToLower(subject), "fwd:") && !strings.HasPrefix(strings.ToLower(subject), "fw:") {
			subject = "Fwd: " + subject
		}
	case "reply", "reply-all":
		if !strings.HasPrefix(strings.ToLower(subject), "re:") {
			subject = "Re: " + subject
		}
	}

	// Build the To and Cc lists based on mode
	var to, cc []smtp.Address
	selfEmails := make(map[string]bool)
	for _, id := range identities {
		selfEmails[strings.ToLower(strings.TrimSpace(id.Email))] = true
	}

	// Prefer Reply-To over From per RFC 5322
	originalFrom := []smtp.Address{{Name: msg.FromName, Address: strings.TrimSpace(msg.FromEmail)}}
	if msg.ReplyTo != "" {
		originalFrom = []smtp.Address{{Address: strings.TrimSpace(msg.ReplyTo)}}
	}

	switch mode {
	case "reply":
		// Reply to sender only
		to = filterSelfAddresses(originalFrom, selfEmails)

		// Defensive fix: if reply resulted in no recipients (replying to self),
		// include the original sender anyway
		if len(to) == 0 && len(originalFrom) > 0 {
			to = originalFrom
		}
	case "reply-all":
		// Reply to sender
		to = filterSelfAddresses(originalFrom, selfEmails)
		// Include original To recipients (excluding self)
		originalTo := parseAddressList(msg.ToList)
		to = append(to, filterSelfAddresses(originalTo, selfEmails)...)
		// Include original Cc recipients (excluding self and duplicates from To)
		originalCc := parseAddressList(msg.CcList)
		toSet := make(map[string]bool)
		for _, addr := range to {
			toSet[strings.ToLower(strings.TrimSpace(addr.Address))] = true
		}
		for _, addr := range filterSelfAddresses(originalCc, selfEmails) {
			if !toSet[strings.ToLower(strings.TrimSpace(addr.Address))] {
				cc = append(cc, addr)
			}
		}

		// Defensive fix: if reply-all resulted in no recipients at all,
		// include the original sender even if it matches a self email
		// (this can happen when replying to your own sent messages)
		if len(to) == 0 && len(cc) == 0 && len(originalFrom) > 0 {
			to = originalFrom
		}
	case "forward":
		// Leave To empty for user to fill in
	}

	// If body hasn't been fetched yet, fetch it on-demand — likewise
	// when a previous fetch failed (BodyFailed), so a reply to a failed
	// message still gets a quote instead of an empty one (R10).
	if !msg.BodyFetched || msg.BodyFailed {
		log.Debug().Str("messageID", messageID).Msg("Body not yet fetched, fetching on-demand for reply")
		updatedMsg, fetchErr := a.syncEngine.FetchMessageBody(a.ctx, msg.AccountID, messageID)
		if fetchErr != nil {
			log.Warn().Err(fetchErr).Msg("Failed to fetch body on-demand, reply will have empty quote")
		} else if updatedMsg != nil {
			msg = updatedMsg
		}
	}

	// Build the quoted body
	dateStr := msg.Date.Format("Mon, Jan 2 2006 at 3:04:05 PM MST")
	sender := msg.FromEmail
	if msg.FromName != "" {
		sender = msg.FromName + " <" + msg.FromEmail + ">"
	}

	// For plain-text-only emails, convert text to HTML for quoting
	quotedHTML := msg.BodyHTML
	if quotedHTML == "" && msg.BodyText != "" {
		quotedHTML = "<p>" + strings.ReplaceAll(escapeHTML(msg.BodyText), "\n", "<br>") + "</p>"
	}

	// Block remote images in quoted content to prevent tracking pixels.
	// Original URLs are preserved in data-original-src for restoration on send.
	// The frontend can unblock them if the sender is in the image allowlist.
	quotedHTML = email.BlockRemoteImages(quotedHTML)

	// Plaintext quote source: prefer the original's text part, but fall back to
	// deriving it from HTML so HTML-only originals still produce a real quote
	// (msg.BodyText is empty for HTML-only mail). Consumed by the plaintext composer.
	quotedText := msg.BodyText
	if strings.TrimSpace(quotedText) == "" && msg.BodyHTML != "" {
		quotedText = email.ExtractPlainTextFromHTML(msg.BodyHTML)
	}

	var htmlBody, textBody string
	if mode == "forward" {
		// Forward format
		htmlBody = fmt.Sprintf("<p></p><p></p><p>---------- Forwarded message ----------<br>From: %s<br>Subject: %s<br>Date: %s<br>To: %s</p><p></p>%s",
			escapeHTML(sender), escapeHTML(msg.Subject), escapeHTML(dateStr), escapeHTML(msg.ToList), quotedHTML)
		textBody = fmt.Sprintf("\n\n---------- Forwarded message ----------\nFrom: %s\nSubject: %s\nDate: %s\nTo: %s\n\n%s",
			sender, msg.Subject, dateStr, msg.ToList, quotedText)
	} else {
		// Reply format
		citation := fmt.Sprintf("On %s, %s wrote:", dateStr, sender)
		htmlBody = fmt.Sprintf("<p></p><p></p><p>%s</p><blockquote type=\"cite\">%s</blockquote>", escapeHTML(citation), quotedHTML)
		textBody = fmt.Sprintf("\n\n%s\n%s", citation, quoteText(quotedText))
	}

	// Build References header per RFC 5322:
	// References = parent's References + parent's Message-ID
	refs := threadReferences(msg)

	// Fetch inline attachments so cid: references in quoted HTML render correctly
	var attachments []smtp.Attachment
	inlineMap, inlineErr := a.attachmentStore.GetInlineByMessage(messageID)
	if inlineErr != nil {
		log.Warn().Err(inlineErr).Msg("Failed to get inline attachments for reply/forward")
	}
	// Map cid -> the stored row's real filename. Re-attached parts used to
	// carry the raw Content-ID as filename, and Outlook-style ids
	// (…@…prod.outlook.com) look like forbidden ".com" files to receiving
	// servers, bouncing the whole reply (#370).
	inlineNames := map[string]string{}
	if rows, rowsErr := a.attachmentStore.GetByMessage(messageID); rowsErr == nil {
		for _, row := range rows {
			if row.ContentID != "" && row.Filename != "" {
				inlineNames[row.ContentID] = row.Filename
			}
		}
	}
	for cid, dataURL := range inlineMap {
		// Replies include an "inline" attachment only when the quoted HTML
		// actually embeds it — mailers stamp Content-IDs on ordinary document
		// attachments, and re-attaching those sends phantom copies with the
		// cid as filename (#381). Forwards keep every part (the forwarder
		// wants the documents).
		if mode != "forward" && !quotedHTMLReferencesCID(quotedHTML, cid) {
			continue
		}
		ct, b64 := parseDataURL(dataURL)
		if b64 == "" {
			continue
		}
		filename := inlineNames[cid]
		if filename == "" {
			filename = email.FallbackFilename(ct)
		}
		attachments = append(attachments, smtp.Attachment{
			ContentBase64: b64,
			ContentType:   ct,
			ContentID:     cid,
			Inline:        true,
			Filename:      sanitizeAttachmentFilename(filename),
		})
	}

	// For forwards, also include regular (non-inline) attachments
	if mode == "forward" {
		a.fetchForwardAttachments(log, msg, &attachments)
	}

	return &smtp.ComposeMessage{
		From:        from,
		To:          to,
		Cc:          cc,
		Subject:     subject,
		HTMLBody:    htmlBody,
		TextBody:    textBody,
		InReplyTo:   ensureAngleBrackets(msg.MessageID),
		References:  refs,
		Attachments: attachments,
	}, nil
}

// quotedHTMLReferencesCID reports whether the quoted HTML actually references
// the given (bracket-stripped) Content-ID. Guards against re-attaching
// misclassified "inline" documents that the reply body never embeds (#381).
// Plain substring match errs toward keeping an attachment when the cid
// appears anywhere in the body, regardless of quote style.
func quotedHTMLReferencesCID(html, cid string) bool {
	return strings.Contains(html, "cid:"+cid)
}

// fetchForwardAttachments fetches regular (non-inline) attachment content from IMAP
// and appends them to the attachments slice. Failures are logged but not fatal.
func (a *App) fetchForwardAttachments(log zerolog.Logger, msg *message.Message, attachments *[]smtp.Attachment) {
	allAtts, err := a.attachmentStore.GetByMessage(msg.ID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get attachments for forward")
		return
	}

	// Filter to non-inline attachments only
	var regularAtts []*message.Attachment
	for _, att := range allAtts {
		if !att.IsInline {
			regularAtts = append(regularAtts, att)
		}
	}
	if len(regularAtts) == 0 {
		return
	}

	// Fetch raw message from IMAP once for all attachments
	raw, fetchErr := a.syncEngine.FetchRawMessage(a.ctx, msg.AccountID, msg.FolderID, msg.UID)
	if fetchErr != nil {
		log.Warn().Err(fetchErr).Msg("Failed to fetch raw message for forward attachments (offline?)")
		return
	}

	downloader := email.NewAttachmentDownloader(a.paths.AttachmentsPath())
	for _, att := range regularAtts {
		content, extractErr := downloader.ExtractAttachmentContent(raw, att.Filename)
		if extractErr != nil {
			log.Warn().Err(extractErr).Str("filename", att.Filename).Msg("Failed to extract attachment for forward")
			continue
		}
		*attachments = append(*attachments, smtp.Attachment{
			Content:     content,
			ContentType: att.ContentType,
			Filename:    att.Filename,
			Inline:      false,
		})
	}
}

// parseDataURL extracts the content type and base64 data from a data URL.
// e.g., "data:image/png;base64,ABC123..." → ("image/png", "ABC123...")
func parseDataURL(dataURL string) (contentType, base64Data string) {
	// Strip "data:" prefix
	rest := strings.TrimPrefix(dataURL, "data:")
	// Split on ";base64,"
	idx := strings.Index(rest, ";base64,")
	if idx < 0 {
		return "", ""
	}
	return rest[:idx], rest[idx+8:]
}

// TestSMTPConnection tests SMTP connection settings
func (a *App) TestSMTPConnection(host string, port int, security, username, password, authMechanism string) error {
	log := logging.WithComponent("app")

	// Map security string to type
	var securityType smtp.SecurityType
	switch security {
	case "none":
		securityType = smtp.SecurityNone
	case "starttls":
		securityType = smtp.SecurityStartTLS
	case "tls":
		securityType = smtp.SecurityTLS
	default:
		securityType = smtp.SecurityStartTLS
	}

	// Create client config
	config := smtp.DefaultConfig()
	config.Host = host
	config.Port = port
	config.Security = securityType
	config.Username = username
	config.Password = password
	config.AuthType = smtp.AuthTypePassword
	config.AuthMechanism = authMechanism
	config.TLSConfig = certificate.BuildTLSConfig(host, a.certStore)

	client := smtp.NewClient(config)

	// Test connection
	if err := client.Connect(); err != nil {
		log.Error().Err(err).Msg("SMTP connection test failed")
		return fmt.Errorf("connection failed: %w", err)
	}
	defer client.Close()

	// Test authentication
	if err := client.Login(); err != nil {
		log.Error().Err(err).Msg("SMTP login test failed")
		return fmt.Errorf("authentication failed: %w", err)
	}

	log.Info().Str("host", host).Int("port", port).Msg("SMTP connection test successful")
	return nil
}

// PickAttachmentFiles opens a file picker dialog and returns the selected files as attachments
func (a *App) PickAttachmentFiles() ([]ComposerAttachment, error) {
	return pickAttachmentFiles(a.ctx)
}

// ReadFileAsAttachment reads a file and creates a ComposerAttachment
func (a *App) ReadFileAsAttachment(filePath string) (*ComposerAttachment, error) {
	return readFileAsAttachment(filePath)
}

// ============================================================================
// Helper Functions
// ============================================================================

// selectReplyFromIdentity picks the From identity for a reply/forward: prefer the
// identity the original message was addressed to (To/Cc/Bcc) so the reply goes out
// from the alias that received the mail (#325); then the default identity; then the
// first. Returns nil only when there are no identities.
func selectReplyFromIdentity(identities []*account.Identity, msg *message.Message) *account.Identity {
	var recipients []smtp.Address
	recipients = append(recipients, parseAddressList(msg.ToList)...)
	recipients = append(recipients, parseAddressList(msg.CcList)...)
	recipients = append(recipients, parseAddressList(msg.BccList)...)
	for _, id := range identities {
		for _, r := range recipients {
			if strings.EqualFold(strings.TrimSpace(id.Email), strings.TrimSpace(r.Address)) {
				return id
			}
		}
	}
	for _, id := range identities {
		if id.IsDefault {
			return id
		}
	}
	if len(identities) > 0 {
		return identities[0]
	}
	return nil
}

// parseAddressList parses a JSON array of addresses or comma-separated string
func parseAddressList(s string) []smtp.Address {
	if s == "" {
		return nil
	}

	// Try smtp.Address JSON format first (uses "address" field) —
	// this is what addressListToJSON stores for drafts
	var smtpAddrs []smtp.Address
	if err := json.Unmarshal([]byte(s), &smtpAddrs); err == nil {
		// Check if the addresses actually have data (not just zero values)
		if len(smtpAddrs) > 0 && smtpAddrs[0].Address != "" {
			return smtpAddrs
		}
	}

	// Try message.Address JSON format (uses "email" field) —
	// this is what the message store uses
	var msgAddrs []message.Address
	if err := json.Unmarshal([]byte(s), &msgAddrs); err == nil {
		var addrs []smtp.Address
		for _, msgAddr := range msgAddrs {
			addrs = append(addrs, smtp.Address{
				Name:    strings.TrimSpace(msgAddr.Name),
				Address: strings.TrimSpace(msgAddr.Email),
			})
		}
		if len(addrs) > 0 && addrs[0].Address != "" {
			return addrs
		}
	}

	// Try as comma-separated list (legacy format)
	var result []smtp.Address
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Parse "Name <email>" format
		if strings.Contains(part, "<") {
			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start > 0 && end > start {
				name := strings.TrimSpace(part[:start])
				email := strings.TrimSpace(part[start+1 : end])
				result = append(result, smtp.Address{Name: name, Address: email})
				continue
			}
		}
		result = append(result, smtp.Address{Address: strings.TrimSpace(part)})
	}
	return result
}

// filterSelfAddresses removes the user's own addresses from a list
func filterSelfAddresses(addrs []smtp.Address, selfEmails map[string]bool) []smtp.Address {
	var result []smtp.Address
	for _, addr := range addrs {
		lowerAddr := strings.ToLower(strings.TrimSpace(addr.Address))
		if !selfEmails[lowerAddr] {
			result = append(result, addr)
		}
	}
	return result
}

// escapeHTML escapes special HTML characters
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

// threadReferences builds the References header for a reply to msg per
// RFC 5322: the parent's References plus the parent's Message-ID.
func threadReferences(msg *message.Message) []string {
	var refs []string
	if msg.References != "" {
		// References are stored as a JSON array in the DB
		_ = json.Unmarshal([]byte(msg.References), &refs)
	}
	if msg.MessageID != "" {
		refs = append(refs, ensureAngleBrackets(msg.MessageID))
	}
	return refs
}

// ensureAngleBrackets wraps a Message-ID in angle brackets if not already present
func ensureAngleBrackets(msgID string) string {
	if msgID == "" {
		return ""
	}
	msgID = strings.TrimSpace(msgID)
	if !strings.HasPrefix(msgID, "<") {
		msgID = "<" + msgID
	}
	if !strings.HasSuffix(msgID, ">") {
		msgID = msgID + ">"
	}
	return msgID
}

// quoteText adds > prefix to each line for plain text quoting
func quoteText(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

// providerAutoSavesSentMail checks if a mail provider automatically saves sent messages
func providerAutoSavesSentMail(host string) bool {
	host = strings.ToLower(host)
	autoSaveProviders := []string{
		"imap.gmail.com",        // Gmail
		"outlook.office365.com", // Microsoft 365
		"imap-mail.outlook.com", // Outlook.com
	}
	for _, provider := range autoSaveProviders {
		if strings.Contains(host, provider) {
			return true
		}
	}
	return false
}

// addressListToJSON converts an address list to JSON string
func addressListToJSON(addrs []smtp.Address) string {
	if len(addrs) == 0 {
		return ""
	}
	data, _ := json.Marshal(addrs)
	return string(data)
}

// sanitizeAttachmentFilename strips path separators and control characters
// from a sender-supplied filename before it's emitted in outgoing MIME
// headers, and caps its length. Defense-in-depth — ToRFC822 already quotes
// the value (filename=%q), so this guards odd receivers, not our emission.
func sanitizeAttachmentFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if runes := []rune(out); len(runes) > 180 {
		out = string(runes[:180])
	}
	if out == "" {
		return "attachment.bin"
	}
	return out
}

// replyToToJSON converts a draft's single Reply-To address to its stored
// JSON form ("" when unset)
func replyToToJSON(addr *smtp.Address) string {
	if addr == nil || addr.Address == "" {
		return ""
	}
	data, _ := json.Marshal(addr)
	return string(data)
}

// parseReplyTo restores a draft's stored Reply-To JSON (nil when unset)
func parseReplyTo(s string) *smtp.Address {
	if s == "" {
		return nil
	}
	var addr smtp.Address
	if err := json.Unmarshal([]byte(s), &addr); err != nil || addr.Address == "" {
		return nil
	}
	return &addr
}

// detectContentType returns the MIME type for a file based on extension
func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	// Images
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".bmp":
		return "image/bmp"
	// Documents
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".odt":
		return "application/vnd.oasis.opendocument.text"
	case ".ods":
		return "application/vnd.oasis.opendocument.spreadsheet"
	case ".odp":
		return "application/vnd.oasis.opendocument.presentation"
	// Text
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js":
		return "text/javascript"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".csv":
		return "text/csv"
	case ".md":
		return "text/markdown"
	// Archives
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz", ".gzip":
		return "application/gzip"
	case ".7z":
		return "application/x-7z-compressed"
	case ".rar":
		return "application/vnd.rar"
	// Audio
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".ogg":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	// Video
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".avi":
		return "video/x-msvideo"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	default:
		return "application/octet-stream"
	}
}
