package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/oauth2"
)

// newOAuthTestApp builds a minimal App with a real temp DB, account
// store and credential store — enough for the pending-token consume
// paths (CompleteOAuthAccountSetup / SavePendingOAuthTokens) without
// a browser, Wails context, or network.
func newOAuthTestApp(t *testing.T) *App {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	credStore, err := credentials.NewStore(db.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{
		db:           db,
		accountStore: account.NewStore(db),
		credStore:    credStore,
	}
}

func pendingTokens(email, provider string) (*oauth2.TokenResponse, string, string) {
	return &oauth2.TokenResponse{
		AccessToken:  "access-" + provider,
		RefreshToken: "refresh-" + provider,
		ExpiresIn:    3600,
	}, email, provider
}

func (a *App) seedPendingOAuth(t *testing.T, email, provider string) {
	t.Helper()
	tokens, e, p := pendingTokens(email, provider)
	a.pendingOAuthTokens = tokens
	a.pendingOAuthEmail = e
	a.pendingOAuthProvider = p
}

// TestCompleteOAuthAccountSetup_RejectsEmailMismatch (fourth-audit
// finding 15): tokens granted to one account must never be saved
// under another account's name — a mismatch is a hard rejection,
// not a warning, and the pending slot is cleared.
func TestCompleteOAuthAccountSetup_RejectsEmailMismatch(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "alice@example.com", "google")
	acc, err := a.CompleteOAuthAccountSetup("google", "bob@example.com", "Bob", "Bob", "")
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("CompleteOAuthAccountSetup = (%v, %v), want a mismatch error", acc, err)
	}
	if a.pendingOAuthTokens != nil {
		t.Fatal("pending tokens were not cleared after the rejected setup")
	}
	accounts, err := a.accountStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %d, want 0 (no account created on mismatch)", len(accounts))
	}
}

// TestCompleteOAuthAccountSetup_RejectsProviderMismatch: pending
// tokens from one provider's sign-in cannot be consumed by another
// provider's setup call.
func TestCompleteOAuthAccountSetup_RejectsProviderMismatch(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "alice@example.com", "microsoft")
	acc, err := a.CompleteOAuthAccountSetup("google", "alice@example.com", "Alice", "Alice", "")
	if err == nil || !strings.Contains(err.Error(), "microsoft") {
		t.Fatalf("CompleteOAuthAccountSetup = (%v, %v), want a provider-mismatch error", acc, err)
	}
	if a.pendingOAuthTokens != nil {
		t.Fatal("pending tokens were not cleared after the rejected setup")
	}
	accounts, err := a.accountStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts = %d, want 0", len(accounts))
	}
}

// TestCompleteOAuthAccountSetup_RefusesUnverifiableEmail: when the
// sign-in produced no email at all, there is no identity to verify
// against — refuse rather than save blind.
func TestCompleteOAuthAccountSetup_RefusesUnverifiableEmail(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "", "google")
	if _, err := a.CompleteOAuthAccountSetup("google", "alice@example.com", "Alice", "Alice", ""); err == nil {
		t.Fatal("CompleteOAuthAccountSetup with no verifiable pending email should fail")
	}
	if a.pendingOAuthTokens != nil {
		t.Fatal("pending tokens were not cleared after the refused setup")
	}
}

// TestCompleteOAuthAccountSetup_HappyPath: matching email and
// provider creates the account, saves its tokens, and clears the
// pending slot.
func TestCompleteOAuthAccountSetup_HappyPath(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "alice@example.com", "google")
	acc, err := a.CompleteOAuthAccountSetup("google", "alice@example.com", "Alice", "Alice", "")
	if err != nil {
		t.Fatalf("CompleteOAuthAccountSetup: %v", err)
	}
	if acc == nil || acc.Email != "alice@example.com" {
		t.Fatalf("account = %+v, want alice@example.com", acc)
	}
	if a.pendingOAuthTokens != nil || a.pendingOAuthEmail != "" || a.pendingOAuthProvider != "" {
		t.Fatal("pending slot was not cleared after setup")
	}
	tokens, err := a.credStore.GetOAuthTokens(acc.ID)
	if err != nil {
		t.Fatalf("GetOAuthTokens: %v", err)
	}
	if tokens.Provider != "google" || tokens.AccessToken != "access-google" {
		t.Fatalf("saved tokens = %+v, want google tokens", tokens)
	}
}

// TestSavePendingOAuthTokens_RejectsProviderMismatch: re-authorizing
// an existing account with another provider's pending tokens must
// not overwrite that account's tokens.
func TestSavePendingOAuthTokens_RejectsProviderMismatch(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "alice@example.com", "google")
	acc, err := a.CompleteOAuthAccountSetup("google", "alice@example.com", "Alice", "Alice", "")
	if err != nil {
		t.Fatal(err)
	}
	a.seedPendingOAuth(t, "alice@example.com", "microsoft")
	if err := a.SavePendingOAuthTokens(acc.ID); err == nil {
		t.Fatal("SavePendingOAuthTokens with another provider's tokens should fail")
	}
	if a.pendingOAuthTokens != nil {
		t.Fatal("pending tokens were not cleared after the rejected save")
	}
	tokens, err := a.credStore.GetOAuthTokens(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-google" {
		t.Fatalf("account tokens changed to %+v, want the original google tokens", tokens)
	}
}

// TestSavePendingOAuthTokens_RejectsEmailMismatch: re-authorizing
// with a different account's tokens must not overwrite this
// account's tokens.
func TestSavePendingOAuthTokens_RejectsEmailMismatch(t *testing.T) {
	a := newOAuthTestApp(t)
	a.seedPendingOAuth(t, "alice@example.com", "google")
	acc, err := a.CompleteOAuthAccountSetup("google", "alice@example.com", "Alice", "Alice", "")
	if err != nil {
		t.Fatal(err)
	}
	a.seedPendingOAuth(t, "mallory@example.com", "google")
	if err := a.SavePendingOAuthTokens(acc.ID); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("SavePendingOAuthTokens = %v, want a mismatch error", err)
	}
	tokens, err := a.credStore.GetOAuthTokens(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-google" {
		t.Fatalf("account tokens changed to %+v, want the original google tokens", tokens)
	}
}
