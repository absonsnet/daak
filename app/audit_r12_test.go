package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/oauth2"
)

// newR12TestSetup mirrors audit_23_test.go: one account with an expired
// token under a stub custom OAuth provider whose token endpoint rotates
// the refresh token. Returns the composeOps under test, the credential
// store, and a counter of token-endpoint calls.
func newR12TestSetup(t *testing.T) (*composeOps, *credentials.Store, *atomic.Int32) {
	t.Helper()

	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const accID = "acc-r12"
	if _, err := db.Exec(`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username, auth_type) VALUES (?, 'Me', ?, 'imap.example.com', 'smtp.example.com', ?, ?)`,
		accID, "me@example.com", "me@example.com", "oauth2"); err != nil {
		t.Fatal(err)
	}

	credStore, err := credentials.NewStore(db.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := credStore.SetOAuthTokens(accID, &credentials.OAuthTokens{
		Provider:     "custom",
		AccessToken:  "access-old",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-1 * time.Hour),
		Scopes:       []string{"scope1"},
	}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-new",
			"refresh_token": "refresh-2",
			"expires_in":    3600,
			"token_type":    "Bearer",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	if err := credStore.SetCustomOAuthProvider(accID, credentials.CustomOAuthProvider{
		AuthURL:  "https://auth.example.com/authorize",
		TokenURL: tokenSrv.URL,
		Scopes:   []string{"scope1"},
		ClientID: "test-client-id",
	}); err != nil {
		t.Fatal(err)
	}

	ops := &composeOps{
		credStore:     credStore,
		oauth2Manager: oauth2.NewManager(),
	}
	return ops, credStore, &calls
}

// TestRefreshSaveFailureFailsVisibly (audit finding R12): when saving the
// refreshed tokens fails (injected store failure), getValidOAuthToken must
// return an error instead of succeeding on the in-memory tokens —
// succeeding would leave the already-revoked refresh token in storage and
// the next refresh would fail invalid_grant. The error must NOT present
// itself as a re-authorization failure: the grant is fine.
func TestRefreshSaveFailureFailsVisibly(t *testing.T) {
	ops, credStore, calls := newR12TestSetup(t)

	saveErr := errors.New("simulated keyring failure")
	ops.saveRefreshedTokensOverride = func(accountID string, tokens *credentials.OAuthTokens) error {
		return saveErr
	}

	tokens, err := ops.getValidOAuthToken(context.Background(), "acc-r12")
	if err == nil {
		t.Fatalf("expected error when saving refreshed tokens fails, got nil (tokens=%v)", tokens)
	}
	if !strings.Contains(err.Error(), "could not be saved") {
		t.Errorf("error = %q, want it to say the tokens could not be saved", err.Error())
	}
	if strings.Contains(err.Error(), "re-authorization required") {
		t.Errorf("error = %q, must not present a save failure as re-authorization required", err.Error())
	}
	// The refresh itself ran exactly once — the failure is in the save.
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint calls = %d, want 1", got)
	}
	// Stored tokens are untouched (still the old, now-revoked pair) —
	// the caller must know the save failed rather than believe the new
	// tokens were persisted.
	stored, err := credStore.GetOAuthTokens("acc-r12")
	if err != nil {
		t.Fatalf("GetOAuthTokens after failed save: %v", err)
	}
	if stored.AccessToken != "access-old" || stored.RefreshToken != "refresh-1" {
		t.Errorf("stored tokens = access %q refresh %q, want the old pair untouched", stored.AccessToken, stored.RefreshToken)
	}
}

// TestRefreshSaveRetryThenSuccess (audit finding R12): a transient save
// failure is retried, and the refresh succeeds once the save lands.
func TestRefreshSaveRetryThenSuccess(t *testing.T) {
	ops, credStore, _ := newR12TestSetup(t)

	var saves atomic.Int32
	ops.saveRefreshedTokensOverride = func(accountID string, tokens *credentials.OAuthTokens) error {
		if saves.Add(1) < 3 {
			return errors.New("simulated transient keyring failure")
		}
		return credStore.SetOAuthTokens(accountID, tokens)
	}

	tokens, err := ops.getValidOAuthToken(context.Background(), "acc-r12")
	if err != nil {
		t.Fatalf("getValidOAuthToken returned error after transient save failures: %v", err)
	}
	if tokens == nil || tokens.AccessToken != "access-new" {
		t.Fatalf("access token = %v, want access-new", tokens)
	}
	if got := saves.Load(); got != 3 {
		t.Errorf("save attempts = %d, want 3 (two failures, then success)", got)
	}
	stored, err := credStore.GetOAuthTokens("acc-r12")
	if err != nil {
		t.Fatalf("GetOAuthTokens after refresh: %v", err)
	}
	if stored.AccessToken != "access-new" || stored.RefreshToken != "refresh-2" {
		t.Errorf("stored tokens = access %q refresh %q, want access-new / refresh-2", stored.AccessToken, stored.RefreshToken)
	}
}

// TestSaveRefreshedTokensWithRetry exercises the retry helper directly:
// it stops at the first success and returns the last error after
// exhausting its attempts.
func TestSaveRefreshedTokensWithRetry(t *testing.T) {
	attempts := 0
	if err := saveRefreshedTokensWithRetry(func() error {
		attempts++
		if attempts < 2 {
			return errors.New("boom")
		}
		return nil
	}); err != nil {
		t.Fatalf("expected nil after a retry succeeds, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}

	boom := errors.New("persistent failure")
	attempts = 0
	err := saveRefreshedTokensWithRetry(func() error {
		attempts++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the persistent failure", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

// TestRefreshSaveFailureIsNotReauth ensures the concurrent-callers guard
// still holds when the save fails: concurrent callers must all see the
// save error (not a mix of success-on-stale-tokens and error).
func TestRefreshSaveFailureConcurrentCallers(t *testing.T) {
	ops, _, _ := newR12TestSetup(t)
	ops.saveRefreshedTokensOverride = func(accountID string, tokens *credentials.OAuthTokens) error {
		return errors.New("simulated keyring failure")
	}

	const callers = 4
	var wg sync.WaitGroup
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = ops.getValidOAuthToken(context.Background(), "acc-r12")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("caller %d: expected save error, got nil", i)
		}
	}
}
