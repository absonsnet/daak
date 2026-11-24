package message

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/database"
)

// newUpsertTestStore opens a migrated temp DB, seeds one (account, folder)
// row, and returns the message Store plus the DB handle for direct assertions.
func newUpsertTestStore(t *testing.T) (*Store, *database.DB, string, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const accountID = "acct-1"
	const folderID = "folder-1"

	if _, err := db.Exec(
		`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		accountID, "Test", "test@example.com", "imap.example.com", "smtp.example.com", "test@example.com",
	); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		folderID, accountID, "INBOX", "INBOX", "inbox",
	); err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	return NewStore(db), db, accountID, folderID
}

// TestUpsert_PreservesBodyAndIDOnConflict is the audit finding-10 regression
// test: a header-only upsert against a row whose body was already fetched
// must not wipe the body, reset body_fetched, change the row id, or strand
// child rows. Before the fix the ON CONFLICT clause set id=excluded.id plus
// the body columns, so this upsert either failed outright (foreign key: the
// reaction row references the old id, no ON UPDATE CASCADE) or silently
// blanked the fetched body.
func TestUpsert_PreservesBodyAndIDOnConflict(t *testing.T) {
	store, db, accountID, folderID := newUpsertTestStore(t)

	// Seed a body-fetched message, as header sync + body fetch leave it.
	orig := &Message{
		AccountID: accountID, FolderID: folderID, UID: 42,
		MessageID: "<orig@example.com>", Subject: "Original subject",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date:     time.Now().UTC().Add(-time.Hour),
		BodyText: "fetched body", BodyHTML: "<p>fetched body</p>",
		Snippet: "fetched body", BodyFetched: true, HasAttachments: true,
	}
	if err := store.Upsert(orig); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
	origID := orig.ID
	origReceivedAt := orig.ReceivedAt

	// A child row referencing the message (a reaction). With the old
	// Upsert this row made the conflicting upsert fail with a foreign-key
	// error: the id changed underneath it with no ON UPDATE CASCADE.
	if _, err := db.Exec(
		`INSERT INTO reactions (id, message_id, target_message_id, reactor_email, emoji, source_message_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"reaction-1", origID, origID, "bob@example.com", "👍", "<reaction@example.com>",
	); err != nil {
		t.Fatalf("seed reaction: %v", err)
	}

	// A header-only re-sync of the same (folder, uid): fresh id, empty
	// body, BodyFetched=false — exactly what the header path passes.
	header := &Message{
		AccountID: accountID, FolderID: folderID, UID: 42,
		MessageID: "<orig@example.com>", Subject: "Updated subject",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(header); err != nil {
		t.Fatalf("conflicting Upsert: %v", err)
	}

	// The conflicting upsert must resolve to the stored id, not a fresh one.
	if header.ID != origID {
		t.Fatalf("Upsert resolved to a new id %q, want stored id %q", header.ID, origID)
	}
	got, err := store.Get(origID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BodyText != "fetched body" || got.BodyHTML != "<p>fetched body</p>" {
		t.Fatalf("body overwritten by header-only upsert: text=%q html=%q", got.BodyText, got.BodyHTML)
	}
	if !got.BodyFetched {
		t.Fatalf("body_fetched reset by header-only upsert")
	}
	if got.Snippet != "fetched body" {
		t.Fatalf("snippet overwritten by header-only upsert: %q", got.Snippet)
	}
	if !got.HasAttachments {
		t.Fatalf("has_attachments cleared by header-only upsert")
	}
	if got.Subject != "Updated subject" {
		t.Fatalf("header column not updated: subject=%q", got.Subject)
	}
	if !got.ReceivedAt.Equal(origReceivedAt) {
		t.Fatalf("received_at changed by header-only upsert: was %v, now %v", origReceivedAt, got.ReceivedAt)
	}
	var reactionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reactions WHERE message_id = ?`, origID).Scan(&reactionCount); err != nil {
		t.Fatalf("count reactions: %v", err)
	}
	if reactionCount != 1 {
		t.Fatalf("reaction child row lost: count=%d", reactionCount)
	}
}

// TestUpsert_InsertsNewRow confirms the basic insert path still works after
// the conflict-column change.
func TestUpsert_InsertsNewRow(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 7,
		MessageID: "<new@example.com>", Subject: "New",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if m.ID == "" {
		t.Fatal("Upsert did not assign an id")
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Subject != "New" || got.UID != 7 {
		t.Fatalf("inserted row mismatch: subject=%q uid=%d", got.Subject, got.UID)
	}
}
