package oauth2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// testFlowProvider builds a minimal provider config for flow tests. It
// points at local test servers only — no live provider is involved.
func testFlowProvider(tokenURL string) *ProviderConfig {
	return &ProviderConfig{
		Name:        "test-provider",
		DisplayName: "Test",
		AuthURL:     "https://auth.example.com/authorize",
		TokenURL:    tokenURL,
		Scopes:      []string{"scope1"},
		ClientID:    "test-client-id",
	}
}

func TestStartAuthFlowRejectsSecondFlow(t *testing.T) {
	m := NewManager()
	ctx := context.Background()

	authURL, err := m.StartAuthFlowWithProvider(ctx, testFlowProvider("https://token.example.com/token"))
	if err != nil {
		t.Fatalf("first StartAuthFlowWithProvider: %v", err)
	}
	if authURL == "" {
		t.Fatal("first flow: empty auth URL")
	}
	if !m.HasActiveSession() {
		t.Fatal("first flow: HasActiveSession = false, want true")
	}
	t.Cleanup(m.CancelAuthFlow)

	// A second flow while the first is waiting must be rejected with a
	// clear error — it must NOT kill the first flow's callback server.
	_, err = m.StartAuthFlowWithProvider(ctx, testFlowProvider("https://token.example.com/token"))
	if err == nil {
		t.Fatal("second flow: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("second flow error = %q, want it to say the flow is already in progress", err.Error())
	}
	if !m.HasActiveSession() {
		t.Error("second flow rejected but first flow is gone: HasActiveSession = false, want true")
	}

	// Cancelling clears the session, so a new flow can start afterwards.
	m.CancelAuthFlow()
	if m.HasActiveSession() {
		t.Fatal("after CancelAuthFlow: HasActiveSession = true, want false")
	}
	if _, err := m.StartAuthFlowWithProvider(ctx, testFlowProvider("https://token.example.com/token")); err != nil {
		t.Fatalf("flow after cancel: %v", err)
	}
}

func TestStartAuthFlowConcurrentOnlyOneSucceeds(t *testing.T) {
	m := NewManager()
	ctx := context.Background()
	t.Cleanup(m.CancelAuthFlow)

	const callers = 8
	var wg sync.WaitGroup
	successes := make(chan struct{}, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := m.StartAuthFlowWithProvider(ctx, testFlowProvider("https://token.example.com/token")); err != nil {
				errs <- err
				return
			}
			successes <- struct{}{}
		}()
	}
	close(start)
	wg.Wait()
	close(successes)
	close(errs)

	if n := len(successes); n != 1 {
		t.Fatalf("concurrent starts: %d succeeded, want exactly 1", n)
	}
	for err := range errs {
		if !strings.Contains(err.Error(), "already in progress") {
			t.Errorf("rejected start error = %q, want it to say the flow is already in progress", err.Error())
		}
	}
}

func TestFailedFlowLeavesNoStaleSession(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		m := NewManager()
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("start flow: %v", err)
		}

		waitCtx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, _, err := m.WaitForCallback(waitCtx)
			done <- err
		}()
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("WaitForCallback after cancel: expected error, got nil")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WaitForCallback did not return after context cancel")
		}

		if m.HasActiveSession() {
			t.Error("failed flow: HasActiveSession = true, want false (stale session left behind)")
		}
		if m.callbackServer != nil {
			t.Error("failed flow: callbackServer != nil, want nil (stale server left behind)")
		}
		// A new flow must be startable after the failed one.
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("flow after failed flow: %v", err)
		}
		m.CancelAuthFlow()
	})

	t.Run("state mismatch", func(t *testing.T) {
		m := NewManager()
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("start flow: %v", err)
		}
		port := m.activeSession.RedirectPort

		done := make(chan error, 1)
		go func() {
			_, _, err := m.WaitForCallback(context.Background())
			done <- err
		}()

		// Deliver a callback with the wrong state.
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=some-code&state=wrong-state", port))
		if err != nil {
			t.Fatalf("deliver callback: %v", err)
		}
		_ = resp.Body.Close()

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("WaitForCallback with wrong state: expected error, got nil")
			}
			if !strings.Contains(err.Error(), "state mismatch") {
				t.Errorf("WaitForCallback error = %q, want a state mismatch error", err.Error())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("WaitForCallback did not return after callback delivery")
		}

		if m.HasActiveSession() {
			t.Error("state-mismatch flow: HasActiveSession = true, want false (stale session left behind)")
		}
		if m.callbackServer != nil {
			t.Error("state-mismatch flow: callbackServer != nil, want nil (stale server left behind)")
		}
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("flow after state-mismatch flow: %v", err)
		}
		m.CancelAuthFlow()
	})
}

func TestSuccessfulFlowClearsSession(t *testing.T) {
	// Token endpoint stub: returns tokens whose ID token carries the email,
	// so no userinfo call is needed.
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com"}`)) + ".sig"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-new",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"id_token":      idToken,
		})
	}))
	defer tokenSrv.Close()

	m := NewManager()
	if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider(tokenSrv.URL)); err != nil {
		t.Fatalf("start flow: %v", err)
	}
	state := m.activeSession.State
	port := m.activeSession.RedirectPort

	done := make(chan error, 1)
	var gotTokens *TokenResponse
	var gotEmail string
	go func() {
		tokens, email, err := m.WaitForCallback(context.Background())
		gotTokens, gotEmail = tokens, email
		done <- err
	}()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=test-code&state=%s", port, state))
	if err != nil {
		t.Fatalf("deliver callback: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForCallback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForCallback did not return after callback delivery")
	}
	if gotTokens == nil || gotTokens.AccessToken != "access-new" {
		t.Fatalf("tokens = %+v, want access token access-new", gotTokens)
	}
	if gotEmail != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", gotEmail)
	}
	if m.HasActiveSession() {
		t.Error("successful flow: HasActiveSession = true, want false")
	}
	if m.callbackServer != nil {
		t.Error("successful flow: callbackServer != nil, want nil")
	}
}

// TestWaitForCallbackDeferredClearDoesNotKillRetry is the regression
// test for re-audit finding R13. The deferred clear in
// WaitForCallback used to drop whatever session was current when it
// ran — not the session it waited on. Cancel clears the session at
// once, so a retry can start while the old waiter is still inside
// WaitForCallback (here: blocked in the token exchange), and the old
// waiter's deferred clear then wiped the retry's session and stopped
// its callback server. The clear is now identity-checked, so the
// retry must survive the old waiter finishing.
//
// The token endpoint blocks until the test releases it, which pins
// the old waiter inside WaitForCallback deterministically: its
// deferred clear cannot run before the retry has started.
func TestWaitForCallbackDeferredClearDoesNotKillRetry(t *testing.T) {
	exchangeStarted := make(chan struct{})
	release := make(chan struct{})
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"email":"user@example.com"}`)) + ".sig"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(exchangeStarted)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-new",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"id_token":      idToken,
		})
	}))
	defer tokenSrv.Close()

	m := NewManager()
	if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider(tokenSrv.URL)); err != nil {
		t.Fatalf("start flow: %v", err)
	}
	oldState := m.activeSession.State
	oldPort := m.activeSession.RedirectPort

	done := make(chan error, 1)
	go func() {
		_, _, err := m.WaitForCallback(context.Background())
		done <- err
	}()

	// Deliver the callback so the waiter reaches the token exchange.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=test-code&state=%s", oldPort, oldState))
	if err != nil {
		t.Fatalf("deliver callback: %v", err)
	}
	_ = resp.Body.Close()

	// Wait until the waiter is blocked inside the token exchange.
	select {
	case <-exchangeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not reach the token exchange")
	}

	// Cancel the old flow and retry immediately, while the old
	// waiter is still inside WaitForCallback.
	m.CancelAuthFlow()
	if m.HasActiveSession() {
		t.Fatal("after CancelAuthFlow: HasActiveSession = true, want false")
	}
	if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider(tokenSrv.URL)); err != nil {
		t.Fatalf("retry after cancel: %v", err)
	}
	newState := m.activeSession.State
	newPort := m.activeSession.RedirectPort
	if newState == oldState {
		t.Fatal("retry reused the old session (same state)")
	}
	t.Cleanup(m.CancelAuthFlow)

	// Let the old waiter finish: its deferred clear must not touch
	// the retry's session or server.
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("old WaitForCallback: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old WaitForCallback did not return after token release")
	}
	if !m.HasActiveSession() {
		t.Fatal("old flow's deferred clear wiped the retry's session: HasActiveSession = false, want true")
	}
	if m.activeSession.State != newState || m.activeSession.RedirectPort != newPort {
		t.Fatalf("active session = state %q port %d, want the retry's state %q port %d",
			m.activeSession.State, m.activeSession.RedirectPort, newState, newPort)
	}
	if m.callbackServer == nil {
		t.Fatal("old flow's deferred clear stopped the retry's callback server")
	}
}

// TestCancelThenImmediateRetryKeepsNewSession covers R13 through the
// exact user sequence: cancel a sign-in, then retry at once. The old
// waiter's deferred clear fires as it unwinds from the stopped
// server; if it runs after the retry started, it used to wipe the
// retry. The interleaving is scheduling-dependent, so the sequence
// is repeated — before the fix the old clear routinely landed after
// the retry started and killed it.
func TestCancelThenImmediateRetryKeepsNewSession(t *testing.T) {
	for i := 0; i < 25; i++ {
		m := NewManager()
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("iter %d: start flow: %v", i, err)
		}
		done := make(chan error, 1)
		go func() {
			_, _, err := m.WaitForCallback(context.Background())
			done <- err
		}()
		// Let the waiter enter WaitForCallback for the old flow
		// first: without this, the goroutine might only be
		// scheduled after the retry has started, and attach to
		// the NEW flow's session instead of the old one. (In the
		// app the waiter is launched at sign-in time, long before
		// any cancel can arrive, so this is a test-only ordering.)
		time.Sleep(100 * time.Millisecond)
		m.CancelAuthFlow()
		if _, err := m.StartAuthFlowWithProvider(context.Background(), testFlowProvider("https://token.example.com/token")); err != nil {
			t.Fatalf("iter %d: retry after cancel: %v", i, err)
		}
		newState := m.activeSession.State
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iter %d: old WaitForCallback did not return after cancel", i)
		}
		if !m.HasActiveSession() {
			t.Fatalf("iter %d: old flow's deferred clear wiped the retry's session", i)
		}
		if m.activeSession.State != newState {
			t.Fatalf("iter %d: active session state %q, want the retry's %q", i, m.activeSession.State, newState)
		}
		m.CancelAuthFlow()
	}
}
