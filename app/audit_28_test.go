package app

import (
	"testing"

	"github.com/absonsnet/daak/internal/message"
)

// Audit finding 28 (v0.7.38 batch): unsubscribe could be offered on
// Sent/Drafts mail when the folder lookup failed — the Inbox/Spam-only
// gate sat inside the successful-lookup branch, so an unresolvable
// folder fell through to the header check and set Available = true.
// The status must default to unavailable when the folder cannot be
// resolved.
//
// These messages are built in memory, not seeded rows: the messages
// table has a foreign key on folder_id, so a row pointing at a missing
// folder cannot exist in the DB — the unresolvable case only arises
// for a folder deleted (or never synced) after the message was read,
// which is exactly what statusForMessage must handle.
func TestUnsubscribeStatusUnresolvableFolder(t *testing.T) {
	uta := newUnsubscribeTestApp(t)
	d := uta.deps(nil)
	lu := "<https://news.example.com/unsub>"

	// Folder id that resolves to nothing.
	ghost := d.statusForMessage(&message.Message{
		ID: "ghost-1", AccountID: "acc-1", FolderID: "missing-folder",
		FromEmail: "news@example.com", ListUnsubscribe: lu,
	})
	if ghost.Available {
		t.Errorf("missing-folder status = %+v, want unavailable (folder cannot be resolved)", ghost)
	}

	// No folder at all.
	noFolder := d.statusForMessage(&message.Message{
		ID: "nofolder-1", AccountID: "acc-1", FolderID: "",
		FromEmail: "news@example.com", ListUnsubscribe: lu,
	})
	if noFolder.Available {
		t.Errorf("empty-folder status = %+v, want unavailable", noFolder)
	}

	// Control: the same headers on a real inbox message are offered.
	uta.seedMessage(t, "inbox-1", "inbox-1", "news@example.com", lu, false)
	stList := d.getStatuses([]string{"inbox-1"})
	if len(stList) != 1 || !stList[0].Available {
		t.Fatalf("inbox status = %+v, want available (control case)", stList)
	}
}
