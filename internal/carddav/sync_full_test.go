package carddav

import (
	"database/sql"
	"testing"

	"github.com/absonsnet/daak/internal/contact"
)

// Finding 2: a full sync used to delete all local contacts and then upsert
// the server's set in separate calls, swallowing upsert failures and still
// advancing the sync token — one transient DB failure silently lost the
// whole addressbook. ReplaceRecordsForAddressbook does delete + upsert +
// token advance in one transaction; these tests prove a failed re-insert
// leaves the old contacts and the old token untouched.
//
// The failure is injected with a test-only SQLite trigger that aborts any
// INSERT of a record with a chosen display name — a stand-in for a transient
// DB failure mid-batch.

// seedRecordEntries seeds records via the normal batch path so the
// addressbook holds real rows + carddav_record_state rows.
func seedRecordEntries(t *testing.T, s *Store, abID string, entries []RecordSyncEntry) {
	t.Helper()
	if err := s.UpsertRecordsBatch(entries); err != nil {
		t.Fatalf("seed UpsertRecordsBatch: %v", err)
	}
}

func countCardDAVRecords(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_records WHERE source = 'carddav'`).Scan(&n); err != nil {
		t.Fatalf("count records: %v", err)
	}
	return n
}

func recordFNs(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT fn FROM contact_records WHERE source = 'carddav'`)
	if err != nil {
		t.Fatalf("list fns: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			t.Fatalf("scan fn: %v", err)
		}
		names[fn] = true
	}
	return names
}

func TestReplaceRecordsForAddressbook_FailedUpsertKeepsOldContactsAndToken(t *testing.T) {
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
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One", Emails: []contact.RecordEmail{{Email: "one@example.com"}}}, AddressbookID: ab.ID, Href: "/old1.vcf", ETag: `"e1"`},
		{Record: &contact.Record{Fn: "Old Two", Emails: []contact.RecordEmail{{Email: "two@example.com"}}}, AddressbookID: ab.ID, Href: "/old2.vcf", ETag: `"e2"`},
	})
	if got := countCardDAVRecords(t, db.DB); got != 2 {
		t.Fatalf("seeded records = %d, want 2", got)
	}

	// Fail any insert of the incoming record named "New Boom".
	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_boom BEFORE INSERT ON contact_records
		WHEN NEW.fn = 'New Boom'
		BEGIN SELECT RAISE(ABORT, 'simulated upsert failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	err = store.ReplaceRecordsForAddressbook(ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "New Boom", Emails: []contact.RecordEmail{{Email: "boom@example.com"}}}, AddressbookID: ab.ID, Href: "/new1.vcf", ETag: `"n1"`},
		{Record: &contact.Record{Fn: "New OK", Emails: []contact.RecordEmail{{Email: "ok@example.com"}}}, AddressbookID: ab.ID, Href: "/new2.vcf", ETag: `"n2"`},
	}, "new-token")
	if err == nil {
		t.Fatal("ReplaceRecordsForAddressbook with a failing upsert should return an error, got nil")
	}

	// Old contacts must be intact — the delete rolled back with the failed insert.
	if got := countCardDAVRecords(t, db.DB); got != 2 {
		t.Fatalf("records after failed replace = %d, want 2 (old contacts intact)", got)
	}
	names := recordFNs(t, db.DB)
	if !names["Old One"] || !names["Old Two"] {
		t.Fatalf("record names after failed replace = %v, want Old One + Old Two", names)
	}
	if names["New Boom"] || names["New OK"] {
		t.Fatalf("record names after failed replace = %v, want no new records", names)
	}

	// The token must not have advanced: the next sync retries the full sync.
	abAfter, err := store.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	if abAfter.SyncToken != "old-token" {
		t.Fatalf("sync token after failed replace = %q, want %q", abAfter.SyncToken, "old-token")
	}
}

func TestReplaceRecordsForAddressbook_SuccessReplacesAndAdvancesToken(t *testing.T) {
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
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old One"}, AddressbookID: ab.ID, Href: "/old1.vcf", ETag: `"e1"`},
	})

	if err := store.ReplaceRecordsForAddressbook(ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "New One", Emails: []contact.RecordEmail{{Email: "new@example.com"}}}, AddressbookID: ab.ID, Href: "/new1.vcf", ETag: `"n1"`},
	}, "new-token"); err != nil {
		t.Fatalf("ReplaceRecordsForAddressbook: %v", err)
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after replace = %d, want 1", got)
	}
	names := recordFNs(t, db.DB)
	if !names["New One"] || names["Old One"] {
		t.Fatalf("record names after replace = %v, want only New One", names)
	}
	abAfter, err := store.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	if abAfter.SyncToken != "new-token" {
		t.Fatalf("sync token after replace = %q, want %q", abAfter.SyncToken, "new-token")
	}
}

// TestSyncAddressbookLegacy_FailedUpsertKeepsOldContactsAndToken drives the
// legacy full-sync path (addressbook-query → PROPFIND enumeration) against
// the Mailfence-style fake server with a failing upsert: the pre-existing
// contact and the pre-existing sync token must both survive.
func TestSyncAddressbookLegacy_FailedUpsertKeepsOldContactsAndToken(t *testing.T) {
	m := newMailfenceServer(false)
	defer m.close()

	db := openCardDAVTestDB(t)
	store := NewStore(db.DB)
	syncer := NewSyncer(store, nil)

	source, err := store.CreateSource(&SourceConfig{Name: "mf", Type: SourceTypeCardDAV, URL: m.srv.URL, Username: "user", Enabled: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ab, err := store.CreateAddressbook(source.ID, "/addressbook/", "Mike Test", true)
	if err != nil {
		t.Fatalf("CreateAddressbook: %v", err)
	}
	if err := store.UpdateAddressbookSyncToken(ab.ID, "old-token"); err != nil {
		t.Fatalf("UpdateAddressbookSyncToken: %v", err)
	}
	seedRecordEntries(t, store, ab.ID, []RecordSyncEntry{
		{Record: &contact.Record{Fn: "Old Contact", Emails: []contact.RecordEmail{{Email: "old@example.com"}}}, AddressbookID: ab.ID, Href: "/old.vcf", ETag: `"e-old"`},
	})

	// The server returns Alice + Bob; fail Alice's insert.
	if _, err := db.DB.Exec(`
		CREATE TRIGGER fail_alice BEFORE INSERT ON contact_records
		WHEN NEW.fn = 'Alice Test'
		BEGIN SELECT RAISE(ABORT, 'simulated upsert failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	client := newTestClient(t, m.srv.URL)
	if err := syncer.syncAddressbookLegacy(client, ab); err == nil {
		t.Fatal("syncAddressbookLegacy with a failing upsert should return an error, got nil")
	}

	if got := countCardDAVRecords(t, db.DB); got != 1 {
		t.Fatalf("records after failed legacy sync = %d, want 1 (old contact intact)", got)
	}
	names := recordFNs(t, db.DB)
	if !names["Old Contact"] {
		t.Fatalf("record names after failed legacy sync = %v, want Old Contact", names)
	}
	abAfter, err := store.GetAddressbook(ab.ID)
	if err != nil {
		t.Fatalf("GetAddressbook: %v", err)
	}
	if abAfter.SyncToken != "old-token" {
		t.Fatalf("sync token after failed legacy sync = %q, want %q", abAfter.SyncToken, "old-token")
	}
}
