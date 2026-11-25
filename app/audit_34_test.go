package app

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/oauth2"
)

// TestContactSourceRefreshLock_IsPerSource (fourth-audit finding 34):
// the refresh lock is one mutex per standalone contact source —
// concurrent refreshes for the SAME source serialise, while different
// sources refresh independently.
func TestContactSourceRefreshLock_IsPerSource(t *testing.T) {
	a := &App{}
	if a.contactSourceRefreshLock("src-1") != a.contactSourceRefreshLock("src-1") {
		t.Fatal("same source returned different refresh locks")
	}
	if a.contactSourceRefreshLock("src-1") == a.contactSourceRefreshLock("src-2") {
		t.Fatal("different sources shared one refresh lock")
	}
}

// TestGetValidContactSourceOAuthToken_ConcurrentFreshToken (fourth-audit
// finding 34): concurrent callers with a fresh token all succeed via
// the re-read fast path — the per-source lock must not deadlock or
// serialize the happy path into failures.
func TestGetValidContactSourceOAuthToken_ConcurrentFreshToken(t *testing.T) {
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
	// Contact-source tokens FK to a contact_sources row.
	carddavStore := carddav.NewStore(db.DB)
	source, err := carddavStore.CreateSource(&carddav.SourceConfig{
		Name: "Test Contacts", Type: carddav.SourceTypeGoogle,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := source.ID
	if err := credStore.SetContactSourceOAuthTokens(sourceID, &credentials.OAuthTokens{
		Provider:     "google",
		AccessToken:  "access-fresh",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	a := &App{
		db:            db,
		credStore:     credStore,
		oauth2Manager: oauth2.NewManager(),
	}

	const callers = 8
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = a.getValidContactSourceOAuthToken(sourceID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
		if tokens[i] != "access-fresh" {
			t.Errorf("caller %d: token = %q, want access-fresh", i, tokens[i])
		}
	}
}
