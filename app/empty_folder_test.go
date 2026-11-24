package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/message"
)

// Finding 7: EmptyTrash / EmptySpam used to take any folderID and
// permanently delete its contents — accountID was never used and the folder
// was never loaded, so a wrong or stale ID could wipe Inbox. Both now
// verify the folder belongs to the account and is of the expected type
// before listing any message IDs.

// seedEmptyFolderDB creates a migrated temp DB with two accounts, an inbox /
// spam / trash folder per account, and one message in each folder.
func seedEmptyFolderDB(t *testing.T, path string) *database.DB {
	t.Helper()
	db, err := database.Open(path)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, acct := range []string{"acct-1", "acct-2"} {
		if _, err := db.Exec(
			`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username) VALUES (?, ?, ?, ?, ?, ?)`,
			acct, "Test "+acct, acct+"@example.com", "imap.example.com", "smtp.example.com", acct+"@example.com",
		); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	for _, f := range [][3]string{
		{"inbox-1", "INBOX", "inbox"},
		{"spam-1", "Spam", "spam"},
		{"trash-1", "Trash", "trash"},
		{"spam-2", "Spam", "spam"},
		{"trash-2", "Trash", "trash"},
	} {
		acct := "acct-1"
		if strings.HasSuffix(f[0], "-2") {
			acct = "acct-2"
		}
		if _, err := db.Exec(
			`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES (?, ?, ?, ?, ?)`,
			f[0], acct, f[1], f[1], f[2],
		); err != nil {
			t.Fatalf("seed folder %s: %v", f[0], err)
		}
	}
	for _, m := range [][3]string{
		{"msg-inbox-1", "inbox-1", "acct-1"},
		{"msg-spam-1", "spam-1", "acct-1"},
		{"msg-trash-1", "trash-1", "acct-1"},
		{"msg-spam-2", "spam-2", "acct-2"},
		{"msg-trash-2", "trash-2", "acct-2"},
	} {
		// UID 0: no server copy yet, so the background IMAP delete skips
		// these rows instead of needing a connection.
		if _, err := db.Exec(
			`INSERT INTO messages (id, account_id, folder_id, uid, message_id, subject, from_name, from_email, to_list, cc_list, bcc_list) VALUES (?, ?, ?, 0, ?, 'Subject', 'Me', 'me@example.com', '[]', '[]', '[]')`,
			m[0], m[2], m[1], "<"+m[0]+"@example.com>",
		); err != nil {
			t.Fatalf("seed message %s: %v", m[0], err)
		}
	}
	return db
}

func newEmptyFolderTestApp(t *testing.T, db *database.DB) *App {
	t.Helper()
	return &App{
		ctx:          context.Background(),
		db:           db,
		accountStore: account.NewStore(db),
		folderStore:  folder.NewStore(db),
		messageStore: message.NewStore(db),
		ownExpungeAt: make(map[string]time.Time),
	}
}

// countMessagesInFolder returns how many messages a folder still holds.
func countMessagesInFolder(t *testing.T, db *database.DB, folderID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE folder_id = ?`, folderID).Scan(&n); err != nil {
		t.Fatalf("count messages in %s: %v", folderID, err)
	}
	return n
}

func TestEmptySpam_WrongAccountRejected(t *testing.T) {
	db := seedEmptyFolderDB(t, filepath.Join(t.TempDir(), "test.db"))
	a := newEmptyFolderTestApp(t, db)

	// spam-2 belongs to acct-2, not acct-1.
	if err := a.EmptySpam("acct-1", "spam-2"); err == nil {
		t.Fatal("EmptySpam with another account's folder should error, got nil")
	}
	if got := countMessagesInFolder(t, db, "spam-2"); got != 1 {
		t.Fatalf("messages in spam-2 = %d, want 1 (nothing deleted)", got)
	}
}

func TestEmptySpam_WrongTypeRejected(t *testing.T) {
	db := seedEmptyFolderDB(t, filepath.Join(t.TempDir(), "test.db"))
	a := newEmptyFolderTestApp(t, db)

	// Inbox must never be emptied through EmptySpam.
	if err := a.EmptySpam("acct-1", "inbox-1"); err == nil {
		t.Fatal("EmptySpam on the Inbox should error, got nil")
	}
	if got := countMessagesInFolder(t, db, "inbox-1"); got != 1 {
		t.Fatalf("messages in inbox-1 = %d, want 1 (nothing deleted)", got)
	}

	// A trash folder must not be emptied through the spam path either.
	if err := a.EmptySpam("acct-1", "trash-1"); err == nil {
		t.Fatal("EmptySpam on a trash folder should error, got nil")
	}
	if got := countMessagesInFolder(t, db, "trash-1"); got != 1 {
		t.Fatalf("messages in trash-1 = %d, want 1 (nothing deleted)", got)
	}
}

func TestEmptyTrash_WrongAccountAndTypeRejected(t *testing.T) {
	db := seedEmptyFolderDB(t, filepath.Join(t.TempDir(), "test.db"))
	a := newEmptyFolderTestApp(t, db)

	if err := a.EmptyTrash("acct-1", "trash-2"); err == nil {
		t.Fatal("EmptyTrash with another account's folder should error, got nil")
	}
	if err := a.EmptyTrash("acct-1", "inbox-1"); err == nil {
		t.Fatal("EmptyTrash on the Inbox should error, got nil")
	}
	if err := a.EmptyTrash("acct-1", "spam-1"); err == nil {
		t.Fatal("EmptyTrash on a spam folder should error, got nil")
	}
	if err := a.EmptyTrash("acct-1", "missing-folder"); err == nil {
		t.Fatal("EmptyTrash on a missing folder should error, got nil")
	}
	for _, f := range []string{"inbox-1", "spam-1", "trash-1", "trash-2"} {
		if got := countMessagesInFolder(t, db, f); got != 1 {
			t.Fatalf("messages in %s = %d, want 1 (nothing deleted)", f, got)
		}
	}
}

// --- Positive cases --------------------------------------------------------
//
// These run in a child test process. DeletePermanently emits a Wails event,
// and the Wails runtime calls log.Fatalf (os.Exit) when the context carries
// no runtime — which a test context never does. The fatal happens AFTER the
// local delete commits, so the parent asserts the DB state left behind
// rather than the child's exit code. If the emit ever becomes safe in
// tests, the child exits 0 and the same assertions still hold.

func TestEmptySpam_Positive(t *testing.T) {
	runEmptyFolderPositive(t, "spam")
}

func TestEmptyTrash_Positive(t *testing.T) {
	runEmptyFolderPositive(t, "trash")
}

func runEmptyFolderPositive(t *testing.T, op string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db := seedEmptyFolderDB(t, dbPath)
	// Close before spawning the child so it can open the same file.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run", `^TestEmptyFolder_PositiveChild$`, "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "DAAK_EMPTY_DB="+dbPath, "DAAK_EMPTY_OP="+op)
	out, _ := cmd.CombinedOutput()
	if strings.Contains(string(out), "FAIL") {
		t.Fatalf("child test failed:\n%s", out)
	}

	// Reopen and assert the outcome. The child may exit 0 (call returned)
	// or 1 (Wails EventsEmit fatal after the local delete committed).
	db2, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db2.Close()
	folderID := op + "-1"
	if got := countMessagesInFolder(t, db2, folderID); got != 0 {
		t.Fatalf("messages in %s = %d, want 0 (folder emptied)", folderID, got)
	}
	// Everything else must be untouched.
	for _, f := range []string{"inbox-1", "spam-1", "trash-1", "spam-2", "trash-2"} {
		if f == folderID {
			continue
		}
		if got := countMessagesInFolder(t, db2, f); got != 1 {
			t.Fatalf("messages in %s = %d, want 1 (untouched)", f, got)
		}
	}
}

func TestEmptyFolder_PositiveChild(t *testing.T) {
	dbPath := os.Getenv("DAAK_EMPTY_DB")
	op := os.Getenv("DAAK_EMPTY_OP")
	if dbPath == "" || op == "" {
		t.Skip("not a child run (DAAK_EMPTY_DB / DAAK_EMPTY_OP unset)")
	}
	db, err := database.Open(dbPath)
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	a := newEmptyFolderTestApp(t, db)
	switch op {
	case "spam":
		if err := a.EmptySpam("acct-1", "spam-1"); err != nil {
			t.Fatalf("EmptySpam: %v", err)
		}
	case "trash":
		if err := a.EmptyTrash("acct-1", "trash-1"); err != nil {
			t.Fatalf("EmptyTrash: %v", err)
		}
	default:
		t.Fatalf("unknown op %q", op)
	}
}
