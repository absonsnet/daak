package message

import (
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/database"
)

// Re-audit finding R15: temp rows left by MoveMessages must only be
// deleted once the destination sync has fetched the real server rows.
// These tests pin the store half of that rule — confirmation by RFC822
// Message-ID (the sync inserts the server copy as a new row, so the
// internal ID cannot identify it) and deletion scoped to just the
// confirmed messages, leaving other in-flight moves untouched.

func seedTempCleanupStore(t *testing.T) (*Store, *database.DB, string, string) {
	t.Helper()
	store, db, accountID, _ := newUpsertTestStore(t)

	const destFolderID = "folder-2"
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		destFolderID, accountID, "Archive", "Archive", "archive",
	); err != nil {
		t.Fatalf("seed folder 2: %v", err)
	}
	return store, db, accountID, destFolderID
}

func seedMovedMessage(t *testing.T, store *Store, accountID, folderID, id, msgID string, uid uint32) *Message {
	t.Helper()
	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: uid,
		MessageID: msgID, Subject: "Moved " + id,
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	m.ID = id
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert %s: %v", id, err)
	}
	return m
}

func TestConfirmedRealCopies_OnlyAfterRealRowArrives(t *testing.T) {
	store, db, accountID, destFolderID := seedTempCleanupStore(t)
	_ = db

	m := seedMovedMessage(t, store, accountID, "folder-1", "m1", "<m1@example.com>", 42)
	if err := store.MoveMessages([]string{m.ID}, destFolderID); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}

	// Temp row only: nothing confirmed.
	ok, err := store.HasRealCopies(destFolderID, []string{"<m1@example.com>"})
	if err != nil {
		t.Fatalf("HasRealCopies: %v", err)
	}
	if ok {
		t.Fatalf("HasRealCopies = true with only the temp row present, want false")
	}

	// The destination sync fetches the server copy as a NEW row (new
	// internal ID, real UID, same Message-ID).
	seedMovedMessage(t, store, accountID, destFolderID, "m1-real", "<m1@example.com>", 99)

	ok, err = store.HasRealCopies(destFolderID, []string{"<m1@example.com>"})
	if err != nil {
		t.Fatalf("HasRealCopies: %v", err)
	}
	if !ok {
		t.Fatalf("HasRealCopies = false with the real row present, want true")
	}

	// A Message-ID with no real row in this folder is not confirmed,
	// and neither is an empty list.
	ok, err = store.HasRealCopies(destFolderID, []string{"<m1@example.com>", "<missing@example.com>"})
	if err != nil {
		t.Fatalf("HasRealCopies: %v", err)
	}
	if ok {
		t.Fatalf("HasRealCopies = true with one ID unconfirmed, want false")
	}
	ok, err = store.HasRealCopies(destFolderID, nil)
	if err != nil {
		t.Fatalf("HasRealCopies: %v", err)
	}
	if ok {
		t.Fatalf("HasRealCopies = true for an empty list, want false")
	}
}

func TestDeleteTempUIDsForMessageIDs_ScopedToConfirmed(t *testing.T) {
	store, db, accountID, destFolderID := seedTempCleanupStore(t)

	m1 := seedMovedMessage(t, store, accountID, "folder-1", "m1", "<m1@example.com>", 42)
	m2 := seedMovedMessage(t, store, accountID, "folder-1", "m2", "<m2@example.com>", 43)
	if err := store.MoveMessages([]string{m1.ID, m2.ID}, destFolderID); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	// Only m1's server copy arrives.
	seedMovedMessage(t, store, accountID, destFolderID, "m1-real", "<m1@example.com>", 99)

	count := func(msgID string) int {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM messages WHERE folder_id = ? AND message_id = ?`,
			destFolderID, msgID,
		).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", msgID, err)
		}
		return n
	}
	if n := count("<m1@example.com>"); n != 2 {
		t.Fatalf("m1 rows in dest before cleanup = %d, want 2 (temp + real duplicate)", n)
	}

	if err := store.DeleteTempUIDsForMessageIDs(destFolderID, []string{"<m1@example.com>"}); err != nil {
		t.Fatalf("DeleteTempUIDsForMessageIDs: %v", err)
	}
	if n := count("<m1@example.com>"); n != 1 {
		t.Fatalf("m1 rows in dest after cleanup = %d, want 1 (real row only, no duplicate)", n)
	}
	// m2's temp row is untouched — its server copy never arrived.
	if n := count("<m2@example.com>"); n != 1 {
		t.Fatalf("m2 rows in dest after cleanup = %d, want 1 (temp row kept)", n)
	}
	var uid int64
	if err := db.QueryRow(`SELECT uid FROM messages WHERE id = ?`, m2.ID).Scan(&uid); err != nil {
		t.Fatalf("m2 uid: %v", err)
	}
	if uid >= 0 {
		t.Fatalf("m2 uid = %d, want negative (temp row preserved)", uid)
	}
}
