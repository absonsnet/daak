package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/carddav"
	"github.com/absonsnet/daak/internal/smtp"
)

// Audit finding 27 (v0.7.38 batch): contacts auto-add to CardDAV checked
// only the first enabled address book, so a recipient already in a second
// book of the same source was duplicated into the first. The check must
// cover every enabled book of the sending account's own source — and
// nothing outside it.

// TestSaveRecipientsCardDAVChecksAllAddressbooks seeds a CardDAV source
// with two enabled books behind a fake server that counts PUTs:
//   - a recipient already in the SECOND book is not created again
//     (no PUT for them, nothing lands in the first book);
//   - a recipient in no book is created exactly once, in the first book;
//   - a recipient who exists only in ANOTHER account's source is still
//     created here — the check never looks outside the sending
//     account's own source.
func TestSaveRecipientsCardDAVChecksAllAddressbooks(t *testing.T) {
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		puts.Add(1)
		w.Header().Set("ETag", `"test-etag"`)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

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
	if err := store.UpsertContact(&carddav.Contact{
		ID: "c-in-second", Email: "second@example.com", DisplayName: "Second Person",
		AddressbookID: ab2.ID, Href: "/ab2/second.vcf",
	}); err != nil {
		t.Fatal(err)
	}

	// A second account whose own source already holds other@example.com.
	otherAcc, err := h.ops.accountStore.Create(&account.AccountConfig{
		Name: "Other", DisplayName: "Other", Email: "other@gmx.net",
		IMAPHost: "imap.gmx.net", SMTPHost: "smtp.gmx.net", Username: "other@gmx.net",
	})
	if err != nil {
		t.Fatal(err)
	}
	otherSource, err := store.CreateSource(&carddav.SourceConfig{
		Name: "Other DAV", Type: carddav.SourceTypeCardDAV, URL: srv.URL,
		Username: "u", AccountID: otherAcc.ID, Enabled: true, Writable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherAB, err := store.CreateAddressbook(otherSource.ID, "/other/", "Other", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertContact(&carddav.Contact{
		ID: "c-in-other", Email: "other@example.com", DisplayName: "Other Person",
		AddressbookID: otherAB.ID, Href: "/other/other.vcf",
	}); err != nil {
		t.Fatal(err)
	}

	h.ops.saveRecipientsToAccountContacts(context.Background(), h.accID, smtp.ComposeMessage{
		To: []smtp.Address{
			{Address: "second@example.com", Name: "Second Person"},
			{Address: "new@example.com", Name: "New Person"},
			{Address: "other@example.com", Name: "Other Person"},
		},
	})

	// Exactly two creates: new + other. The second-book recipient must
	// not produce a PUT at all.
	if got := puts.Load(); got != 2 {
		t.Fatalf("PUTs = %d, want 2 (new + other; second-book recipient must not be created)", got)
	}
	if c, err := store.GetContactByEmailInAddressbook("second@example.com", ab1.ID); err != nil {
		t.Fatal(err)
	} else if c != nil {
		t.Errorf("second@example.com was duplicated into the first book, want it only in the second")
	}
	for _, email := range []string{"new@example.com", "other@example.com"} {
		if c, err := store.GetContactByEmailInAddressbook(email, ab1.ID); err != nil {
			t.Fatal(err)
		} else if c == nil {
			t.Errorf("%s was not created in the first book", email)
		}
		if c, err := store.GetContactByEmailInAddressbook(email, ab2.ID); err != nil {
			t.Fatal(err)
		} else if c != nil {
			t.Errorf("%s also landed in the second book, want exactly one copy in the first", email)
		}
	}
}
