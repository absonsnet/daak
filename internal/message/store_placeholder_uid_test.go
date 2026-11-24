package message

import (
	"testing"
	"time"
)

// TestGetMessageUIDAndFolder_RejectsPlaceholderUID is the audit finding-4
// regression test: MoveMessages writes a negative placeholder UID
// (uid = -rowid) until the destination sync fetches the real UID. The old
// GetMessageUIDAndFolder converted it unchecked to uint32 — a huge bogus
// UID the server can never return — and the body fetch then deleted the
// row as a "ghost". The lookup must refuse a placeholder UID instead, so
// the row survives until the destination sync (plus DeleteTempUIDs)
// resolves it.
func TestGetMessageUIDAndFolder_RejectsPlaceholderUID(t *testing.T) {
	store, db, accountID, folderID := newUpsertTestStore(t)

	const destFolderID = "folder-2"
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		destFolderID, accountID, "Archive", "Archive", "archive",
	); err != nil {
		t.Fatalf("seed folder 2: %v", err)
	}

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 42,
		MessageID: "<moved@example.com>", Subject: "Moved",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	// The normal path still returns the real UID.
	if uid, folder, err := store.GetMessageUIDAndFolder(m.ID); err != nil {
		t.Fatalf("GetMessageUIDAndFolder (real UID): %v", err)
	} else if uid != 42 || folder != folderID {
		t.Fatalf("GetMessageUIDAndFolder = (%d, %q), want (42, %q)", uid, folder, folderID)
	}

	// Move it: the row now carries the negative placeholder UID.
	if err := store.MoveMessages([]string{m.ID}, destFolderID); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}

	// The lookup must refuse the placeholder, not convert it to a huge
	// bogus uint32 (uint32(-rowid)) that no server fetch can ever return.
	if uid, _, err := store.GetMessageUIDAndFolder(m.ID); err == nil {
		t.Fatalf("GetMessageUIDAndFolder accepted a placeholder UID as %d, want an error", uid)
	}

	// And the row itself is untouched — nothing deleted it.
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get after move: %v", err)
	}
	if got.FolderID != destFolderID {
		t.Fatalf("moved row folder = %q, want %q", got.FolderID, destFolderID)
	}
	if got.Subject != "Moved" {
		t.Fatalf("moved row subject = %q, want %q", got.Subject, "Moved")
	}
}
