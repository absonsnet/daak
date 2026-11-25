package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/smtp"
)

// Re-audit R22: contacts auto-add wrote only to the first enabled
// address book and checked writability per source, not per book. When
// the first book was read-only (403 on PUT) the save failed, was only
// logged, and the contact never reached the cloud — while the local
// copy still landed, so nothing looked wrong. The fix tries the next
// writable book, remembers a 403 book as read-only, and surfaces a
// failure in every book to the user instead of logging it silently.
//
// The fake server below answers PUTs per address-book path so the
// tests can make the first book refuse writes while the second
// accepts them.

// r22Server counts PUTs per address-book path prefix and answers each
// prefix with its configured status.
type r22Server struct {
	mu     sync.Mutex
	puts   map[string]int32
	status map[string]int
}

func newR22Server(t *testing.T, status map[string]int) (*httptest.Server, *r22Server) {
	t.Helper()
	s := &r22Server{puts: map[string]int32{}, status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		s.mu.Lock()
		for prefix, st := range s.status {
			if strings.HasPrefix(r.URL.Path, prefix) {
				s.puts[prefix]++
				s.mu.Unlock()
				w.Header().Set("ETag", `"test-etag"`)
				w.WriteHeader(st)
				return
			}
		}
		s.mu.Unlock()
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func (s *r22Server) putCount(prefix string) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts[prefix]
}

// r22Harness seeds a CardDAV source with two enabled books behind the
// fake server. Returns the harness plus the two book rows.
func r22Harness(t *testing.T, srv *httptest.Server) (*accountContactsHarness, *carddav.Addressbook, *carddav.Addressbook) {
	t.Helper()
	h := newAccountContactsHarness(t, account.AuthPassword, "me@gmx.net", "imap.gmx.net", nil)
	store := h.ops.carddavStore
	h.ops.carddavSyncer = carddav.NewSyncer(store, h.ops.credStore)

	source, err := store.CreateSource(&carddav.SourceConfig{
		Name: "Test DAV", Type: carddav.SourceTypeCardDAV, URL: srv.URL,
		Username: "u", AccountID: h.accID, Enabled: true, Writable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ops.credStore.SetCardDAVPassword(source.ID, "pw"); err != nil {
		t.Fatal(err)
	}
	ab1, err := store.CreateAddressbook(source.ID, "/ab1/", "First", true)
	if err != nil {
		t.Fatal(err)
	}
	ab2, err := store.CreateAddressbook(source.ID, "/ab2/", "Second", true)
	if err != nil {
		t.Fatal(err)
	}
	return h, ab1, ab2
}

// TestSaveRecipientsCardDAVFallsThroughToSecondBook is the R22
// regression test: the first book refuses the PUT with 403, the
// second accepts it. The contact must land in the second book, the
// first book must be remembered as read-only, and no failure warning
// fires (the save succeeded, just not in the first book).
func TestSaveRecipientsCardDAVFallsThroughToSecondBook(t *testing.T) {
	srv, counts := newR22Server(t, map[string]int{
		"/ab1/": http.StatusForbidden,
		"/ab2/": http.StatusCreated,
	})
	h, ab1, ab2 := r22Harness(t, srv)

	var failures atomic.Int32
	h.ops.onCardDAVAutoAddFailure = func(sourceID, accountID string, emails []string, err error) {
		failures.Add(1)
	}

	h.ops.saveRecipientsToAccountContacts(context.Background(), h.accID, smtp.ComposeMessage{
		To: []smtp.Address{{Address: "new@example.com", Name: "New Person"}},
	})

	if got := counts.putCount("/ab1/"); got != 1 {
		t.Errorf("PUTs to first (read-only) book = %d, want 1 (tried once, then fell through)", got)
	}
	if got := counts.putCount("/ab2/"); got != 1 {
		t.Errorf("PUTs to second book = %d, want 1", got)
	}
	if c, err := h.ops.carddavStore.GetContactByEmailInAddressbook("new@example.com", ab1.ID); err != nil {
		t.Fatal(err)
	} else if c != nil {
		t.Errorf("new@example.com landed in the read-only first book, want it only in the second")
	}
	if c, err := h.ops.carddavStore.GetContactByEmailInAddressbook("new@example.com", ab2.ID); err != nil {
		t.Fatal(err)
	} else if c == nil {
		t.Errorf("new@example.com was not created in the second book")
	}
	ab1After, err := h.ops.carddavStore.GetAddressbook(ab1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ab1After.Writable {
		t.Errorf("first book still marked writable after its 403, want it remembered as read-only")
	}
	if got := failures.Load(); got != 0 {
		t.Errorf("failure warnings = %d, want 0 (the save succeeded in the second book)", got)
	}
}

// TestSaveRecipientsCardDAVSkipsKnownReadOnlyBook: a book already
// recorded as read-only (e.g. by an earlier 403, or by discovery
// finding no write privilege) is never PUT to at all — the save goes
// straight to the writable book.
func TestSaveRecipientsCardDAVSkipsKnownReadOnlyBook(t *testing.T) {
	srv, counts := newR22Server(t, map[string]int{
		"/ab1/": http.StatusCreated,
		"/ab2/": http.StatusCreated,
	})
	h, ab1, ab2 := r22Harness(t, srv)
	if err := h.ops.carddavStore.SetAddressbookWritable(ab1.ID, false); err != nil {
		t.Fatal(err)
	}

	h.ops.saveRecipientsToAccountContacts(context.Background(), h.accID, smtp.ComposeMessage{
		To: []smtp.Address{{Address: "new@example.com", Name: "New Person"}},
	})

	if got := counts.putCount("/ab1/"); got != 0 {
		t.Errorf("PUTs to known read-only first book = %d, want 0", got)
	}
	if got := counts.putCount("/ab2/"); got != 1 {
		t.Errorf("PUTs to second book = %d, want 1", got)
	}
	if c, err := h.ops.carddavStore.GetContactByEmailInAddressbook("new@example.com", ab2.ID); err != nil {
		t.Fatal(err)
	} else if c == nil {
		t.Errorf("new@example.com was not created in the second book")
	}
}

// TestSaveRecipientsCardDAVAllBooksFailSurfacesFailure: when every
// writable book refuses the write, the failure must reach the user —
// the warning hook fires once with the failed email, and the source
// carries the failure as its last error (visible in Settings →
// Contacts) — instead of only a log line.
func TestSaveRecipientsCardDAVAllBooksFailSurfacesFailure(t *testing.T) {
	srv, _ := newR22Server(t, map[string]int{
		"/ab1/": http.StatusForbidden,
		"/ab2/": http.StatusForbidden,
	})
	h, ab1, ab2 := r22Harness(t, srv)

	var mu sync.Mutex
	var failedEmails []string
	var hookCalls int
	h.ops.onCardDAVAutoAddFailure = func(sourceID, accountID string, emails []string, err error) {
		mu.Lock()
		defer mu.Unlock()
		hookCalls++
		failedEmails = append(failedEmails, emails...)
	}

	h.ops.saveRecipientsToAccountContacts(context.Background(), h.accID, smtp.ComposeMessage{
		To: []smtp.Address{{Address: "new@example.com", Name: "New Person"}},
	})

	mu.Lock()
	calls, emails := hookCalls, append([]string(nil), failedEmails...)
	mu.Unlock()
	if calls != 1 {
		t.Fatalf("failure warnings = %d, want exactly 1", calls)
	}
	if len(emails) != 1 || emails[0] != "new@example.com" {
		t.Fatalf("failed emails = %v, want [new@example.com]", emails)
	}
	for _, ab := range []*carddav.Addressbook{ab1, ab2} {
		if c, err := h.ops.carddavStore.GetContactByEmailInAddressbook("new@example.com", ab.ID); err != nil {
			t.Fatal(err)
		} else if c != nil {
			t.Errorf("new@example.com landed in %q despite every book refusing the write", ab.Name)
		}
		after, err := h.ops.carddavStore.GetAddressbook(ab.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Writable {
			t.Errorf("book %q still marked writable after its 403", ab.Name)
		}
	}
	sources, err := h.ops.carddavStore.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].LastError == "" {
		t.Errorf("source last error = %q, want a recorded auto-add failure (visible in Settings → Contacts)", sources[0].LastError)
	}
}
