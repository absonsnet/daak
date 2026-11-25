package app

import (
	"path/filepath"
	"testing"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/message"
)

// TestViewerAdjustFolderCountsForReadFlip (fourth-audit finding 47):
// a read flip in the viewer popup must move the shared folder rows
// by the same delta the main window applies, so the main window's
// badge can be refreshed from them. Messages already in the target
// state contribute nothing.
func TestViewerAdjustFolderCountsForReadFlip(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO folders (id, account_id, name, path, folder_type, unread_count) VALUES ('inbox-1', 'acc-1', 'INBOX', 'INBOX', 'inbox', 5)`); err != nil {
		t.Fatal(err)
	}

	v := &ViewerApp{folderStore: folder.NewStore(db)}
	msgs := []*message.Message{
		{ID: "m1", FolderID: "inbox-1", IsRead: false},
		{ID: "m2", FolderID: "inbox-1", IsRead: false},
		{ID: "m3", FolderID: "inbox-1", IsRead: true}, // already read: no delta
	}
	counts := v.adjustFolderCountsForReadFlip(map[string][]*message.Message{"inbox-1": msgs}, true)
	if counts["inbox-1"] != 3 {
		t.Fatalf("counts = %v, want inbox-1: 3 (5 unread, 2 flipped to read)", counts)
	}
	f, err := v.folderStore.Get("inbox-1")
	if err != nil || f == nil {
		t.Fatalf("folder Get: %v, %v", f, err)
	}
	if f.UnreadCount != 3 {
		t.Fatalf("stored unread count = %d, want 3", f.UnreadCount)
	}

	// Flipping back to unread restores the count.
	counts = v.adjustFolderCountsForReadFlip(map[string][]*message.Message{"inbox-1": {
		{ID: "m1", FolderID: "inbox-1", IsRead: true},
		{ID: "m2", FolderID: "inbox-1", IsRead: true},
	}}, false)
	if counts["inbox-1"] != 5 {
		t.Fatalf("counts after unread flip = %v, want inbox-1: 5", counts)
	}
}
