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
