package sync

import (
	"testing"

	"github.com/absonsnet/daak/internal/message"
)

// Audit finding 25: the new-mail notification counted the TotalCount
// delta (after − before), so arrivals masked by deletions in the same
// sync were under-counted or never notified — 3 arrivals + 2 deletions
// reported "1 new email", and equal arrivals/deletions reported nothing
// at all. The count must come from the UID-set diff: genuinely inserted
// message IDs. These tests pin that counting rule plus the payload the
// scheduler builds from it (count + the newest inserted message, not
// whichever conversation happens to be newest overall).

func TestDiffNewUIDs_ArrivalsMaskedByDeletions(t *testing.T) {
	// 5 local messages; the server lists 3 of them plus 3 new ones:
	// UIDs 4 and 5 were deleted elsewhere, UIDs 6, 7, 8 arrived.
	local := uids(5)
	remote := []uint32{1, 2, 3, 6, 7, 8}

	newUIDs, deleted, _, _ := reconcileUIDs(local, remote)
	if len(newUIDs) != 3 {
		t.Fatalf("reconcileUIDs new=%d, want 3 (UIDs 6, 7, 8)", len(newUIDs))
	}
	if len(deleted) != 2 {
		t.Fatalf("reconcileUIDs deleted=%d, want 2 (UIDs 4, 5)", len(deleted))
	}

	// The old counting rule: TotalCount after − before.
	totalDelta := len(remote) - len(local)
	if totalDelta != 1 {
		t.Fatalf("totalDelta=%d, want 1 (the old, wrong count for this sync)", totalDelta)
	}

	// The new counting rule: the UID-set diff.
	if got := diffNewUIDs(local, remote); len(got) != 3 {
		t.Fatalf("diffNewUIDs=%d, want 3 new — not the TotalCount delta of 1", len(got))
	}
}

func TestDiffNewUIDs_EqualArrivalsAndDeletions(t *testing.T) {
	// 2 arrivals + 2 deletions: the TotalCount delta is 0, so the old
	// rule notified nothing at all.
	local := uids(5)
	remote := []uint32{1, 2, 3, 6, 7}

	totalDelta := len(remote) - len(local)
	if totalDelta != 0 {
		t.Fatalf("totalDelta=%d, want 0 (the old rule's blind spot)", totalDelta)
	}
	if got := diffNewUIDs(local, remote); len(got) != 2 {
		t.Fatalf("diffNewUIDs=%d, want 2 new", len(got))
	}
}

func TestNewMessageIDs_SkipsUnsavedUIDs(t *testing.T) {
	// UIDs newest-first; UID 7's header fetch failed, so no row was
	// stored for it — it must not count.
	stored := map[uint32]string{8: "msg-8", 6: "msg-6"}
	ids := newMessageIDs([]uint32{8, 7, 6}, func(uid uint32) (string, bool) {
		id, ok := stored[uid]
		return id, ok
	})
	if len(ids) != 2 || ids[0] != "msg-8" || ids[1] != "msg-6" {
		t.Fatalf("newMessageIDs=%v, want [msg-8 msg-6] in newest-first order", ids)
	}
}

func TestNewMailInfoFromResult_CountAndNewest(t *testing.T) {
	res := &SyncResult{
		NewMessageIDs: []string{"msg-8", "msg-7", "msg-6"},
		NewUIDs:       []uint32{8, 7, 6},
		Newest: &message.Message{
			Subject:   "Newest arrival",
			FromName:  "Carol",
			FromEmail: "carol@example.com",
			ThreadID:  "<thread-8@example.com>",
		},
	}
	info := newMailInfoFromResult("acct-1", "Test", "folder-1", res)
	if info == nil {
		t.Fatal("newMailInfoFromResult returned nil for 3 new messages")
	}
	if info.Count != 3 {
		t.Fatalf("Count=%d, want 3", info.Count)
	}
	if info.Subject != "Newest arrival" || info.FromEmail != "carol@example.com" || info.ThreadID != "<thread-8@example.com>" {
		t.Fatalf("notification describes %+v, want the newest inserted message (Carol / Newest arrival)", info)
	}
}

func TestNewMailInfoFromResult_NilWhenNone(t *testing.T) {
	if info := newMailInfoFromResult("acct-1", "Test", "folder-1", &SyncResult{}); info != nil {
		t.Fatalf("newMailInfoFromResult=%+v, want nil when nothing new arrived", info)
	}
	if info := newMailInfoFromResult("acct-1", "Test", "folder-1", nil); info != nil {
		t.Fatalf("newMailInfoFromResult=%+v, want nil for a nil result", info)
	}
}
