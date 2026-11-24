package sync

import (
	"testing"
)

// Audit finding 3: the empty-server safeguard was disabled exactly when a
// sync period was in use (the default 30 days), so a transient empty UID
// SEARCH result deleted the whole folder; and a >50% disappearance only
// logged a warning before deleting anyway. reconcileUIDs is the pure UID
// comparison SyncMessages uses — these tests pin both safeguards plus the
// normal expunge path, which must keep working.

func uids(n uint32) []uint32 {
	out := make([]uint32, 0, n)
	for i := uint32(1); i <= n; i++ {
		out = append(out, i)
	}
	return out
}

func TestReconcileUIDs_EmptyRemoteRefused(t *testing.T) {
	local := uids(5)
	_, deleted, emptyRemote, massDelete := reconcileUIDs(local, nil)
	if !emptyRemote {
		t.Fatal("empty remote listing with local mail must set emptyRemote")
	}
	if massDelete {
		t.Fatal("empty remote with 5 local messages must not also flag massDelete")
	}
	if len(deleted) != 5 {
		t.Fatalf("deleted=%d, want 5 (all local UIDs are missing from the listing)", len(deleted))
	}
	// The caller (SyncMessages) returns early on emptyRemote, so the
	// listed deletions never run — the refusal is the early return.
}

func TestReconcileUIDs_EmptyRemoteNoLocal(t *testing.T) {
	_, deleted, emptyRemote, massDelete := reconcileUIDs(nil, nil)
	if emptyRemote || massDelete {
		t.Fatalf("empty folder both sides must not refuse: emptyRemote=%v massDelete=%v", emptyRemote, massDelete)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted=%d, want 0", len(deleted))
	}
}

func TestReconcileUIDs_MassDeleteRefused(t *testing.T) {
	local := uids(20)
	remote := uids(8) // 12 of 20 missing: more than half
	_, deleted, emptyRemote, massDelete := reconcileUIDs(local, remote)
	if emptyRemote {
		t.Fatal("non-empty remote listing must not set emptyRemote")
	}
	if !massDelete {
		t.Fatal("12 of 20 missing must set massDelete")
	}
	if len(deleted) != 12 {
		t.Fatalf("deleted=%d, want 12", len(deleted))
	}
}

func TestReconcileUIDs_HalfNotRefused(t *testing.T) {
	local := uids(20)
	remote := uids(10) // exactly half missing: not more than half
	_, deleted, _, massDelete := reconcileUIDs(local, remote)
	if massDelete {
		t.Fatal("exactly half missing must not set massDelete")
	}
	if len(deleted) != 10 {
		t.Fatalf("deleted=%d, want 10", len(deleted))
	}
}

func TestReconcileUIDs_SmallFolderNotRefused(t *testing.T) {
	local := uids(5)
	remote := uids(1) // 4 of 5 missing, but the folder is small (<= 10)
	_, deleted, _, massDelete := reconcileUIDs(local, remote)
	if massDelete {
		t.Fatal("small folders (<= 10 messages) are exempt from the mass-delete rule")
	}
	if len(deleted) != 4 {
		t.Fatalf("deleted=%d, want 4", len(deleted))
	}
}

func TestReconcileUIDs_NormalExpungeAllowed(t *testing.T) {
	local := uids(20)
	remote := uids(18) // 2 expunged: the normal path must still delete them
	newUIDs, deleted, emptyRemote, massDelete := reconcileUIDs(local, remote)
	if emptyRemote || massDelete {
		t.Fatalf("normal expunge must not refuse: emptyRemote=%v massDelete=%v", emptyRemote, massDelete)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%d, want 2", len(deleted))
	}
	if len(newUIDs) != 0 {
		t.Fatalf("new=%d, want 0", len(newUIDs))
	}
}

func TestReconcileUIDs_NewUIDsListed(t *testing.T) {
	local := uids(5)
	remote := append(uids(5), 6, 7)
	newUIDs, deleted, _, _ := reconcileUIDs(local, remote)
	if len(newUIDs) != 2 {
		t.Fatalf("new=%d, want 2 (UIDs 6 and 7)", len(newUIDs))
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted=%d, want 0", len(deleted))
	}
}
