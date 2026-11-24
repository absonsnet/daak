package message

import (
	"testing"
	"time"
)

// TestDeleteOlderThan_Survivors is the audit finding-6 regression test: the
// sync-period purge used to delete by header Date, account-wide, with no
// zero-date exclusion — so a dateless message (zero date) or an old-dated
// but recently arrived message was deleted on every sync and re-fetched
// forever, and old mail in folders not being synced was deleted too. The
// purge now compares received_at (first-seen, effectively arrival time),
// is scoped to the folder being synced, and never purges zero/NULL
// received_at rows.
func TestDeleteOlderThan_Survivors(t *testing.T) {
	store, db, accountID, folderID := newUpsertTestStore(t)

	const otherFolderID = "folder-2"
	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES (?, ?, ?, ?, ?)`,
		otherFolderID, accountID, "Archive", "Archive", "archive",
	); err != nil {
		t.Fatalf("seed folder 2: %v", err)
	}

	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -30)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

	seed := func(id string, folder string, uid uint32, date, receivedAt time.Time) {
		t.Helper()
		m := &Message{
			AccountID: accountID, FolderID: folder, UID: uid,
			MessageID: "<" + id + "@example.com>", Subject: id,
			FromName: "Alice", FromEmail: "alice@example.com",
			Date: date, ReceivedAt: receivedAt,
		}
		if err := store.Upsert(m); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// Old by every clock, in the synced folder: the only row the purge may take.
	seed("old", folderID, 1, old, old)
	// Zero header Date, recently received: must survive (was deleted every sync).
	seed("zero-date", folderID, 2, time.Time{}, now)
	// Old header Date, recently arrived: must survive (was deleted every sync).
	seed("old-date-recent", folderID, 3, time.Date(2015, 6, 1, 0, 0, 0, 0, time.UTC), now)
	// Old by every clock, but in another folder: must survive (folder scoping).
	seed("other-folder", otherFolderID, 1, old, old)

	// A row with NULL received_at must survive the purge (zero guard).
	zeroReceived := &Message{
		AccountID: accountID, FolderID: folderID, UID: 4,
		MessageID: "<zero-received@example.com>", Subject: "zero-received",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: old, ReceivedAt: now,
	}
	if err := store.Upsert(zeroReceived); err != nil {
		t.Fatalf("seed zero-received: %v", err)
	}
	if _, err := db.Exec(`UPDATE messages SET received_at = NULL WHERE id = ?`, zeroReceived.ID); err != nil {
		t.Fatalf("null out received_at: %v", err)
	}

	deleted, err := store.DeleteOlderThan(accountID, folderID, cutoff)
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted=%d, want 1 (only the old row in the synced folder)", deleted)
	}

	for _, want := range []string{"zero-date", "old-date-recent", "other-folder", "zero-received"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE subject = ?`, want).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", want, err)
		}
		if count != 1 {
			t.Fatalf("message %q should have survived the purge, count=%d", want, count)
		}
	}
	var oldCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE subject = 'old'`).Scan(&oldCount); err != nil {
		t.Fatalf("count old: %v", err)
	}
	if oldCount != 0 {
		t.Fatal("old message in the synced folder should have been purged")
	}
}
