package message

import (
	"testing"
)

// Audit4 finding 18: a local read/star change whose server push failed
// must not be overwritten by the next sync's flag download, and the
// pending row must survive until the push succeeds.
func TestPendingFlagChanges_GuardSyncOverwrite(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	msg := &Message{
		AccountID: accountID, FolderID: folderID, UID: 7,
		Subject: "hello", IsRead: false, IsStarred: false,
	}
	if err := store.Upsert(msg); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Local flip, as setReadStatus does it: write the flag locally,
	// then record the pending push.
	isRead := true
	if err := store.UpdateFlagsBatch([]string{msg.ID}, &isRead, nil); err != nil {
		t.Fatalf("UpdateFlagsBatch: %v", err)
	}
	if err := store.RecordPendingFlagChanges([]*Message{msg}, FlagTypeRead, true); err != nil {
		t.Fatalf("RecordPendingFlagChanges: %v", err)
	}
	if n, err := store.PendingFlagChangeCount(); err != nil || n != 1 {
		t.Fatalf("PendingFlagChangeCount = %d, %v, want 1", n, err)
	}

	// The server still says unread (its flags lag the local flip).
	// The sync flag download must NOT overwrite the pending read flag,
	// but it may still apply the other flags (starred stays false here).
	if err := store.UpdateFlagsByUIDBatch(folderID, []FlagUpdate{{
		UID: 7, IsRead: false, IsStarred: false,
	}}); err != nil {
		t.Fatalf("UpdateFlagsByUIDBatch: %v", err)
	}
	got, err := store.GetByIDs([]string{msg.ID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(got) != 1 || !got[0].IsRead {
		t.Fatalf("after guarded sync: IsRead = %v, want true (local flip preserved)", got[0].IsRead)
	}

	// A later flip of the same flag replaces the pending row, not a
	// second one — and the row now carries the new value.
	if err := store.RecordPendingFlagChanges([]*Message{msg}, FlagTypeRead, false); err != nil {
		t.Fatalf("RecordPendingFlagChanges (re-flip): %v", err)
	}
	pending, err := store.ListPendingFlagChanges()
	if err != nil {
		t.Fatalf("ListPendingFlagChanges: %v", err)
	}
	if len(pending) != 1 || pending[0].FlagValue {
		t.Fatalf("pending after re-flip = %+v, want one row with FlagValue false", pending)
	}

	// Successful push clears the row; the guard lifts.
	if err := store.DeletePendingFlagChanges([]string{msg.ID}, FlagTypeRead); err != nil {
		t.Fatalf("DeletePendingFlagChanges: %v", err)
	}
	if n, err := store.PendingFlagChangeCount(); err != nil || n != 0 {
		t.Fatalf("PendingFlagChangeCount after delete = %d, %v, want 0", n, err)
	}
	if err := store.UpdateFlagsByUIDBatch(folderID, []FlagUpdate{{
		UID: 7, IsRead: false, IsStarred: false,
	}}); err != nil {
		t.Fatalf("UpdateFlagsByUIDBatch (unguarded): %v", err)
	}
	got, err = store.GetByIDs([]string{msg.ID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(got) != 1 || got[0].IsRead {
		t.Fatalf("after unguarded sync: IsRead = %v, want false (server value applies)", got[0].IsRead)
	}
}
