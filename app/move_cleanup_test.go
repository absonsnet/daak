package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/message"
)

// Re-audit finding R15 tests for the shared post-move step
// (cleanupMovedTempUIDs), used by both the main window (App.MoveToFolder,
// syncDest = App.SyncFolder) and the detached viewer
// (ViewerApp.moveMessagesToFolder, syncDest = the viewer's direct
// syncEngine.SyncMessages call). The full ViewerApp move cannot run in a
// test binary — it emits a Wails event, and Wails' runtime calls log.Fatal
// on a non-Wails context (see undo_e2e_test.go) — so these tests drive the
// extracted step the viewer now calls, with the sync step stubbed in the
// viewer's shape (a plain func() error returning nil or a failure). The
// scenarios are the ones the brief names: a viewer move leaves no temp
// rows and no duplicate, and a failed destination sync preserves the temp
// rows (and the message's local state).

func newMoveCleanupStore(t *testing.T) (*message.Store, *database.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username) VALUES ('acc-1', 'Me', 'me@example.com', 'imap.example.com', 'smtp.example.com', 'me@example.com')`); err != nil {
		t.Fatal(err)
	}
	for _, f := range [][3]string{
		{"inbox-1", "INBOX", "inbox"},
		{"archive-1", "Archive", "archive"},
	} {
		if _, err := db.Exec(`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES (?, 'acc-1', ?, ?, ?)`, f[0], f[1], f[1], f[2]); err != nil {
			t.Fatal(err)
		}
	}
	return message.NewStore(db), db
}

func seedMoveCleanupMessage(t *testing.T, db *database.DB, id, msgID string, uid, isRead int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (id, account_id, folder_id, uid, message_id, subject, from_name, from_email, to_list, cc_list, bcc_list, is_read) VALUES (?, 'acc-1', 'inbox-1', ?, ?, 'Subject', 'Me', 'me@example.com', '[]', '[]', '[]', ?)`, id, uid, msgID, isRead); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// seedRealCopy simulates the destination sync fetching the server copy:
// a new row in the destination folder, real UID, same Message-ID.
func seedRealCopy(t *testing.T, db *database.DB, id, msgID string, uid int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (id, account_id, folder_id, uid, message_id, subject, from_name, from_email, to_list, cc_list, bcc_list, is_read) VALUES (?, 'acc-1', 'archive-1', ?, ?, 'Subject', 'Me', 'me@example.com', '[]', '[]', '[]', 1)`, id, uid, msgID); err != nil {
		t.Fatalf("seed real copy %s: %v", id, err)
	}
}

func moveCleanupState(t *testing.T, db *database.DB, msgID string) (rows, tempRows int, isRead bool) {
	t.Helper()
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder_id = 'archive-1' AND message_id = ?`, msgID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder_id = 'archive-1' AND message_id = ? AND uid < 0`, msgID).Scan(&tempRows); err != nil {
		t.Fatalf("count temp rows: %v", err)
	}
	var read int
	if err := db.QueryRow(`SELECT COALESCE(is_read, 0) FROM messages WHERE folder_id = 'archive-1' AND message_id = ? AND uid < 0`, msgID).Scan(&read); err != nil {
		// No temp row left: read state lives on the real row now.
		read = -1
	}
	return rows, tempRows, read == 1
}

func TestCleanupMovedTempUIDs_SyncSuccessDeletesTempNoDuplicate(t *testing.T) {
	ms, db := newMoveCleanupStore(t)
	seedMoveCleanupMessage(t, db, "msg-1", "<m1@example.com>", 42, 1)
	if err := ms.MoveMessages([]string{"msg-1"}, "archive-1"); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	seedRealCopy(t, db, "msg-1-real", "<m1@example.com>", 99)

	moved := []*message.Message{{ID: "msg-1", MessageID: "<m1@example.com>"}}
	n, err := cleanupMovedTempUIDs(ms, "archive-1", moved, func() error { return nil })
	if err != nil {
		t.Fatalf("cleanupMovedTempUIDs: %v", err)
	}
	if n != 1 {
		t.Fatalf("cleaned = %d, want 1", n)
	}
	rows, tempRows, _ := moveCleanupState(t, db, "<m1@example.com>")
	if rows != 1 || tempRows != 0 {
		t.Fatalf("dest rows = %d (%d temp), want 1 row, 0 temp (no duplicate)", rows, tempRows)
	}
}

func TestCleanupMovedTempUIDs_FailedSyncPreservesTempRows(t *testing.T) {
	ms, db := newMoveCleanupStore(t)
	seedMoveCleanupMessage(t, db, "msg-1", "<m1@example.com>", 42, 1)
	if err := ms.MoveMessages([]string{"msg-1"}, "archive-1"); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}

	moved := []*message.Message{{ID: "msg-1", MessageID: "<m1@example.com>"}}
	n, err := cleanupMovedTempUIDs(ms, "archive-1", moved, func() error {
		return errors.New("IMAP connection lost")
	})
	if err == nil {
		t.Fatalf("cleanupMovedTempUIDs with failed sync returned nil error, want an error")
	}
	if n != 0 {
		t.Fatalf("cleaned = %d, want 0", n)
	}
	rows, tempRows, isRead := moveCleanupState(t, db, "<m1@example.com>")
	if rows != 1 || tempRows != 1 {
		t.Fatalf("dest rows = %d (%d temp), want 1 row, 1 temp (preserved)", rows, tempRows)
	}
	if !isRead {
		t.Fatalf("temp row lost its read flag, want is_read preserved")
	}
}

func TestCleanupMovedTempUIDs_SyncSuccessButUnconfirmedPreservesTempRows(t *testing.T) {
	ms, db := newMoveCleanupStore(t)
	seedMoveCleanupMessage(t, db, "msg-1", "<m1@example.com>", 42, 1)
	if err := ms.MoveMessages([]string{"msg-1"}, "archive-1"); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	// Sync reports success but the server copy never arrived (e.g. the
	// message falls outside the sync window): the temp row must stay,
	// or the message vanishes from the destination folder.
	moved := []*message.Message{{ID: "msg-1", MessageID: "<m1@example.com>"}}
	n, err := cleanupMovedTempUIDs(ms, "archive-1", moved, func() error { return nil })
	if err == nil {
		t.Fatalf("cleanupMovedTempUIDs with unconfirmed copy returned nil error, want an error")
	}
	if n != 0 {
		t.Fatalf("cleaned = %d, want 0", n)
	}
	rows, tempRows, isRead := moveCleanupState(t, db, "<m1@example.com>")
	if rows != 1 || tempRows != 1 || !isRead {
		t.Fatalf("dest rows = %d (%d temp, read=%v), want 1 row, 1 temp, read preserved", rows, tempRows, isRead)
	}
}

func TestCleanupMovedTempUIDs_PartialConfirmationCleansOnlyConfirmed(t *testing.T) {
	ms, db := newMoveCleanupStore(t)
	seedMoveCleanupMessage(t, db, "msg-1", "<m1@example.com>", 42, 1)
	seedMoveCleanupMessage(t, db, "msg-2", "<m2@example.com>", 43, 1)
	if err := ms.MoveMessages([]string{"msg-1", "msg-2"}, "archive-1"); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	seedRealCopy(t, db, "msg-1-real", "<m1@example.com>", 99)

	moved := []*message.Message{
		{ID: "msg-1", MessageID: "<m1@example.com>"},
		{ID: "msg-2", MessageID: "<m2@example.com>"},
	}
	n, err := cleanupMovedTempUIDs(ms, "archive-1", moved, func() error { return nil })
	if err == nil {
		t.Fatalf("cleanupMovedTempUIDs with one unconfirmed copy returned nil error, want an error naming it")
	}
	if n != 1 {
		t.Fatalf("cleaned = %d, want 1 (only the confirmed message)", n)
	}
	rows, tempRows, _ := moveCleanupState(t, db, "<m1@example.com>")
	if rows != 1 || tempRows != 0 {
		t.Fatalf("m1 dest rows = %d (%d temp), want 1 row, 0 temp", rows, tempRows)
	}
	rows, tempRows, _ = moveCleanupState(t, db, "<m2@example.com>")
	if rows != 1 || tempRows != 1 {
		t.Fatalf("m2 dest rows = %d (%d temp), want 1 row, 1 temp (kept)", rows, tempRows)
	}
}
