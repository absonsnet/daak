package message

import (
	"testing"
	"time"
)

// TestUpsertKeepsReadReceiptHandled (fourth-audit finding 58): the
// "receipt already handled" marker is local-only state. A header
// re-sync upserts the same (folder_id, uid) row with a fresh message
// whose default is false; the upsert must not reset the marker, or
// the user gets asked about a receipt they already answered (on a
// batch retry after a partial save, or a server UID reuse).
func TestUpsertKeepsReadReceiptHandled(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	orig := &Message{
		ID: "rr-1", AccountID: accountID, FolderID: folderID,
		UID: 1, MessageID: "<rr1@example.com>", Subject: "Receipt?",
		FromEmail: "a@example.com", Date: now,
		ReadReceiptTo: "a@example.com",
	}
	if err := s.Create(orig); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.MarkReadReceiptHandled(orig.ID); err != nil {
		t.Fatalf("MarkReadReceiptHandled: %v", err)
	}

	// Header re-sync: same (folder, uid), fresh message, handled=false.
	fresh := &Message{
		ID: "rr-1-fresh", AccountID: accountID, FolderID: folderID,
		UID: 1, MessageID: "<rr1@example.com>", Subject: "Receipt?",
		FromEmail: "a@example.com", Date: now,
		ReadReceiptTo: "a@example.com", ReadReceiptHandled: false,
	}
	if err := s.Upsert(fresh); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.Get(orig.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil after upsert")
	}
	if !got.ReadReceiptHandled {
		t.Error("Upsert reset read_receipt_handled to false, want it preserved (true)")
	}
}
