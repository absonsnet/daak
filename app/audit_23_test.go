package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/oauth2"
)

// TestConcurrentOAuthRefreshSingleFlight (audit finding 23): concurrent
// getValidOAuthToken calls for one account with an expired token must
// result in exactly one token-endpoint call and no spurious re-auth error.
//
// The stub token endpoint models refresh-token rotation: it accepts the
// original refresh token once, then rejects it with invalid_grant —
// exactly what a real provider does after rotation. Without the
// per-account refresh lock, two concurrent callers would both present the
// old refresh token and the second would fail with a spurious
// oauth:reauth-required even though a valid token had just been stored.
// The 100ms handler delay widens the overlap window so all callers are
// in flight together.
func TestConcurrentOAuthRefreshSingleFlight(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const accID = "acc-23"
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
	var mu sync.Mutex
	acceptedRefreshToken := "refresh-1"
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		mu.Lock()
		ok := r.Form.Get("refresh_token") == acceptedRefreshToken
		if ok {
			// Rotate: the presented token is now spent.
			acceptedRefreshToken = "refresh-2"
		}
		mu.Unlock()
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
			return
		}
		// Hold the refresh in flight so the other callers overlap it.
		time.Sleep(100 * time.Millisecond)
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

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	tokens := make([]*credentials.OAuthTokens, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = ops.getValidOAuthToken(context.Background(), accID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: getValidOAuthToken returned error: %v (spurious re-auth failure)", i, err)
		}
		if tokens[i] == nil || tokens[i].AccessToken != "access-new" {
			t.Errorf("caller %d: access token = %v, want access-new", i, tokens[i])
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint calls = %d, want exactly 1", got)
	}

	stored, err := credStore.GetOAuthTokens(accID)
	if err != nil {
		t.Fatalf("GetOAuthTokens after refresh: %v", err)
	}
	if stored.AccessToken != "access-new" || stored.RefreshToken != "refresh-2" {
		t.Errorf("stored tokens = access %q refresh %q, want access-new / refresh-2", stored.AccessToken, stored.RefreshToken)
	}
	if stored.IsExpiringSoon(5 * time.Minute) {
		t.Errorf("stored tokens still expiring soon (expires %v)", stored.ExpiresAt)
	}
}
