package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/message"
)

// TestStarExpandsToFolderCopies (fourth-audit finding 43): starring one
// copy of a duplicated message must cover every same-folder copy, exactly
// like read/unread already does. setStarredStatus now expands its input
// through expandToFolderCopies first; this test pins that expansion and
// the resulting flag state for both copies.
//
// Note: setStarredStatus itself cannot run in a unit test — it emits a
// Wails event, and wailsRuntime.EventsEmit fatal-exits without a runtime
// context (same pre-existing constraint as the scheduler callback in
// finding 38). The store-level expansion is covered by
// internal/message's TestExpandToFolderCopies; here the App-level
// helper and the flag write it feeds are proven.
func TestStarExpandsToFolderCopies(t *testing.T) {
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
	if _, err := db.Exec(`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES ('inbox-1', 'acc-1', 'INBOX', 'INBOX', 'inbox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO folders (id, account_id, name, path, folder_type) VALUES ('sent-1', 'acc-1', 'Sent', 'Sent', 'sent')`); err != nil {
		t.Fatal(err)
	}

	store := message.NewStore(db)
	const mid = "<dup@example.com>"
	seed := func(id, folderID string, uid uint32) {
		m := &message.Message{ID: id, AccountID: "acc-1", FolderID: folderID, UID: uid, MessageID: mid, Date: time.Now().UTC()}
		if err := store.Create(m); err != nil {
			t.Fatal(err)
		}
	}
	seed("copy-a", "inbox-1", 10)
	seed("copy-b", "inbox-1", 11)
	seed("copy-sent", "sent-1", 7) // same Message-ID, other folder: untouched

	a := &App{messageStore: store}
	expanded := a.expandToFolderCopies([]string{"copy-a"})
	if len(expanded) != 2 {
		t.Fatalf("expandToFolderCopies([copy-a]) = %v, want both inbox copies", expanded)
	}
	// Mirror setStarredStatus after expansion: the flag write over the
	// expanded ids must star both copies and leave the Sent copy alone.
	starred := true
	if err := store.UpdateFlagsBatch(expanded, nil, &starred); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"copy-a", "copy-b"} {
		m, err := store.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !m.IsStarred {
			t.Errorf("message %s IsStarred = false, want true", id)
		}
	}
	if m, err := store.Get("copy-sent"); err != nil {
		t.Fatal(err)
	} else if m.IsStarred {
		t.Errorf("Sent copy IsStarred = true, want false (other folder untouched)")
	}
}
