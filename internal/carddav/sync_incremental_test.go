package carddav

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/absonsnet/daak/internal/contact"
)

// Re-audit finding R1: the incremental sync used to swallow failures and
// advance the sync token anyway — a failed delete was only logged, and
// UpsertRecordsBatch logged per-record failures, skipped the record, and
// returned nil, so the caller believed the batch landed. The token then
// moved past a change the local store never reached, and the next sync
// never re-asked for it. These tests prove the old token survives a failed
// delete / failed upsert on every incremental path, and that the change is
// retried (a later successful sync lands it and advances the token).
//
// Failures are injected with test-only SQLite triggers that abort the
// offending write — a stand-in for a transient DB failure mid-batch.

// syncCollectionServer is a minimal fake CardDAV server answering
// sync-collection REPORTs with a fixed deleted-href set and token. A
// <status>404</status> response is how go-webdav reports a deleted href.
type syncCollectionServer struct {
	srv      *httptest.Server
	deleted  []string
	newToken string
}

func newSyncCollectionServer(deleted []string, newToken string) *syncCollectionServer {
	m := &syncCollectionServer{deleted: deleted, newToken: newToken}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *syncCollectionServer) close() { m.srv.Close() }

func (m *syncCollectionServer) handle(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 1024)
	if r.Body != nil {
		buf := make([]byte, 64<<10)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		_ = r.Body.Close()
	}
	if r.Method != "REPORT" || !strings.Contains(string(body), "sync-collection") {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` +
		`<d:multistatus xmlns:d="DAV:">` +
		`<d:sync-token>` + m.newToken + `</d:sync-token>`)
	for _, href := range m.deleted {
		fmt.Fprintf(&b,
			`<d:response><d:href>%s</d:href>`+
				`<d:status>HTTP/1.1 404 Not Found</d:status></d:response>`, href)
	}
	b.WriteString(`</d:multistatus>`)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write([]byte(b.String()))
}

func assertSyncToken(t *testing.T, store *Store, abID, want string) {
	t.Helper()
	abAfter, err := store.GetAddressbook(abID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	if abAfter.SyncToken != want {
		t.Fatalf("sync token = %q, want %q", abAfter.SyncToken, want)
	}
}

// TestUpsertRecordsBatch_FailedRecordFailsBatchAndRollsBack: one failing
// record in the batch must fail the whole batch — the sibling update that
// already ran inside the transaction rolls back with it, so the caller
// never advances a token past a half-landed batch.
func TestUpsertRecordsBatch_FailedRecordFailsBatchAndRollsBack(t *testing.T) {
	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeCardDAV, URL: "https://example/", Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/ab/", "Book", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "/old1.vcf", ETag: `"e1"`},
	})

	// Fail any insert of the incoming record named "New Boom".
	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_boom BEFORE INSERT ON contact_records
		WHEN NEW.fn = 'New Boom'
		BEGIN SELECT RAISE(ABORT, 'simulated upsert failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err = store.UpsertRecordsBatch([]RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old Changed", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "/old1.vcf", ETag: `"e1b"`},
		{Record: &contact.Record{Fn: "New Boom", Emails: []contact.RecordEmail{{Email: "boom@example.com"}}}, AddressbookID: ab.ID, Href: "/new1.vcf", ETag: `"n1"`},
	})
	if err == nil {
		t.Fatal("UpsertRecordsBatch with a failing record should return an error, got nil")
	}

	// The batch rolled back: no new record, and the sibling update to the
	// seeded record did not survive either.
	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after failed batch = %d, want 1 (seeded record only)", got)
	}
	names := recordFNs(t, db.DB)
	if !names["Old One"] {
		t.Fatalf("record names after failed batch = %v, want Old One intact (not Old Changed)", names)
	}
	if names["New Boom"] || names["Old Changed"] {
		t.Fatalf("record names after failed batch = %v, want no partially-landed records", names)
	}
}

// TestSyncAddressbookIncremental_FailedDeleteKeepsOldToken drives the
// CardDAV incremental path against a fake server reporting one deleted
// href, with the local delete failing: the sync must error, the record
// must stay, and the token must not advance — so the next sync retries
// the same delete.
func TestSyncAddressbookIncremental_FailedDeleteKeepsOldToken(t *testing.T) {
	m := newSyncCollectionServer([]string{"/ab/old1.vcf"}, "new-token")
	defer m.close()

	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeCardDAV, URL: m.srv.URL, Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/ab/", "Book", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	ab, err = store.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "/ab/old1.vcf", ETag: `"e1"`},
	})

	// Fail any delete of a contact record.
	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_delete BEFORE DELETE ON contact_records
		BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	client := newTestClient(t, m.srv.URL)
	if err := syncer.syncAddressbookIncremental(client, ab); err == nil {
		t.Fatal("syncAddressbookIncremental with a failing delete should return an error, got nil")
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after failed incremental sync = %d, want 1 (deleted contact still local)", got)
	}
	if names := recordFNs(t, db.DB); !names["Old One"] {
		t.Fatalf("record names after failed incremental sync = %v, want Old One", names)
	}
	assertSyncToken(t, store, ab.ID, "old-token")
}

// TestSyncAddressbookIncremental_SuccessDeletesAndAdvancesToken is the
// retry half of the test above: once the delete works, the same delta
// lands the deletion and the token advances.
func TestSyncAddressbookIncremental_SuccessDeletesAndAdvancesToken(t *testing.T) {
	m := newSyncCollectionServer([]string{"/ab/old1.vcf"}, "new-token")
	defer m.close()

	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeCardDAV, URL: m.srv.URL, Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/ab/", "Book", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	ab, err = store.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "/ab/old1.vcf", ETag: `"e1"`},
		{Record: &contact.Record{Fn: "Keeper", Emails: []contact.RecordEmail{{Email: "keep@example.com"}}}, AddressbookID: ab.ID, Href: "/ab/keep.vcf", ETag: `"e2"`},
	})

	client := newTestClient(t, m.srv.URL)
	if err := syncer.syncAddressbookIncremental(client, ab); err != nil {
		t.Fatalf("syncAddressbookIncremental: %v", err)
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after incremental sync = %d, want 1 (Keeper only)", got)
	}
	if names := recordFNs(t, db.DB); !names["Keeper"] || names["Old One"] {
		t.Fatalf("record names after incremental sync = %v, want only Keeper", names)
	}
	assertSyncToken(t, store, ab.ID, "new-token")
}

// TestStoreOAuthContactsDelta_FailedDeleteKeepsOldToken is the Google /
// Microsoft half of R1: a failed delete of a provider-reported removal
// must fail the delta store and leave the old token in place.
func TestStoreOAuthContactsDelta_FailedDeleteKeepsOldToken(t *testing.T) {
	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeGoogle, URL: "https://example/", Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/", "Google", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "google-1", ETag: `"e1"`},
	})

	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_delete BEFORE DELETE ON contact_records
		BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err = syncer.storeOAuthContactsDelta(ab, &contact.SyncResult{
		DeletedIDs:    []string{"google-1"},
		NextSyncToken: "new-token",
	})
	if err == nil {
		t.Fatal("storeOAuthContactsDelta with a failing delete should return an error, got nil")
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after failed OAuth delta = %d, want 1 (deleted contact still local)", got)
	}
	if names := recordFNs(t, db.DB); !names["Old One"] {
		t.Fatalf("record names after failed OAuth delta = %v, want Old One", names)
	}
	assertSyncToken(t, store, ab.ID, "old-token")
}

// TestStoreOAuthContactsDelta_FailedUpsertKeepsOldToken: a failed upsert
// in the OAuth incremental path must fail the delta store and leave the
// old token in place, so the next sync retries the same delta.
func TestStoreOAuthContactsDelta_FailedUpsertKeepsOldToken(t *testing.T) {
	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeMicrosoft, URL: "https://example/", Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/", "Microsoft", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "ms-1", ETag: `"e1"`},
	})

	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_boom BEFORE INSERT ON contact_records
		WHEN NEW.fn = 'New Boom'
		BEGIN SELECT RAISE(ABORT, 'simulated upsert failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err = syncer.storeOAuthContactsDelta(ab, &contact.SyncResult{
		Records: []contact.SyncedRecord{
			{RemoteID: "ms-2", ETag: `"n1"`, Record: &contact.Record{Fn: "New Boom", Emails: []contact.RecordEmail{{Email: "boom@example.com"}}}},
		},
		NextSyncToken: "new-token",
	})
	if err == nil {
		t.Fatal("storeOAuthContactsDelta with a failing upsert should return an error, got nil")
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after failed OAuth delta = %d, want 1 (Old One only)", got)
	}
	if names := recordFNs(t, db.DB); !names["Old One"] || names["New Boom"] {
		t.Fatalf("record names after failed OAuth delta = %v, want only Old One", names)
	}
	assertSyncToken(t, store, ab.ID, "old-token")
}

// TestStoreOAuthContactsDelta_SuccessDeletesAndAdvancesToken is the retry
// half for the OAuth path: once the delete works, the delta lands and the
// token advances.
func TestStoreOAuthContactsDelta_SuccessDeletesAndAdvancesToken(t *testing.T) {
	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "src", Type: SourceTypeGoogle, URL: "https://example/", Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/", "Google", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "google-1", ETag: `"e1"`},
		{Record: &contact.Record{Fn: "Keeper", Emails: []contact.RecordEmail{{Email: "keep@example.com"}}}, AddressbookID: ab.ID, Href: "google-2", ETag: `"e2"`},
	})

	if err := syncer.storeOAuthContactsDelta(ab, &contact.SyncResult{
		DeletedIDs:    []string{"google-1"},
		NextSyncToken: "new-token",
	}); err != nil {
		t.Fatalf("storeOAuthContactsDelta: %v", err)
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after OAuth delta = %d, want 1 (Keeper only)", got)
	}
	if names := recordFNs(t, db.DB); !names["Keeper"] || names["Old One"] {
		t.Fatalf("record names after OAuth delta = %v, want only Keeper", names)
	}
	assertSyncToken(t, store, ab.ID, "new-token")
}
