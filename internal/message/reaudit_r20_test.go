package message

import (
	"testing"
	"time"
)

// Re-audit finding R20 (store half): messageStore.Create never inserted
// the body_truncated column, so a truncated body built by the
// server-search path lost its flag on insert and the viewer showed it
// as complete. Create must persist the flag exactly as UpdateBody does.
func TestCreate_PersistsTruncatedFlag(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	trunc := &Message{
		AccountID: accountID, FolderID: folderID, UID: 51,
		MessageID: "<r20-trunc@example.com>", Subject: "Truncated",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date:     time.Now().UTC(),
		BodyText: "partial body", Snippet: "partial body",
		BodyFetched: true, BodyTruncated: true,
	}
	if err := store.Create(trunc); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Get(trunc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyTruncated {
		t.Fatal("body_truncated=0 after Create for a truncated message, want 1")
	}

	full := &Message{
		AccountID: accountID, FolderID: folderID, UID: 52,
		MessageID: "<r20-full@example.com>", Subject: "Complete",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date:     time.Now().UTC(),
		BodyText: "full body", Snippet: "full body",
		BodyFetched: true,
	}
	if err := store.Create(full); err != nil {
		t.Fatalf("Create (full): %v", err)
	}
	got, err = store.Get(full.ID)
	if err != nil {
		t.Fatalf("Get (full): %v", err)
	}
	if got.BodyTruncated {
		t.Fatal("body_truncated=1 after Create for a complete message, want 0")
	}
}
