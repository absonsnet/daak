package message

import (
	"testing"
	"time"
)

// Audit finding 26 (store layer): a body truncated at the 10 MB part /
// 50 MB message caps must be persisted as body_truncated=1 (migration
// v53) so the viewer can disclose that the stored body is only the first
// part — instead of body_fetched=1 alone, which presented it as
// complete. A later full body persist clears the flag, as does a Force
// Resync (ClearBodiesForFolder).
func TestUpdateBody_PersistsTruncatedFlag(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 42,
		MessageID: "<trunc@example.com>", Subject: "Truncated",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	if err := store.UpdateBody(BodyUpdate{
		MessageID: m.ID, BodyText: "partial body", Snippet: "partial body", Truncated: true,
	}); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyFetched {
		t.Fatal("body_fetched not set by UpdateBody")
	}
	if !got.BodyTruncated {
		t.Fatal("body_truncated not persisted by UpdateBody for a truncated body")
	}
	// The flag must also be visible through the UID lookup the sync
	// engine uses when resolving newly inserted messages.
	gotByUID, err := store.GetByUID(folderID, 42)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if gotByUID == nil || !gotByUID.BodyTruncated {
		t.Fatal("GetByUID BodyTruncated=false, want true")
	}

	// A later full (non-truncated) body persist clears the flag.
	if err := store.UpdateBody(BodyUpdate{
		MessageID: m.ID, BodyText: "full body", Snippet: "full body",
	}); err != nil {
		t.Fatalf("UpdateBody (full): %v", err)
	}
	got, err = store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BodyTruncated {
		t.Fatal("body_truncated still set after a full body persist")
	}
}

func TestUpdateBodiesBatch_PersistsTruncatedFlag(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	seed := func(uid uint32, mid string) *Message {
		m := &Message{
			AccountID: accountID, FolderID: folderID, UID: uid,
			MessageID: mid, Subject: mid,
			FromName: "Alice", FromEmail: "alice@example.com",
			Date: time.Now().UTC(),
		}
		if err := store.Upsert(m); err != nil {
			t.Fatalf("seed Upsert: %v", err)
		}
		return m
	}
	trunc := seed(11, "<t@example.com>")
	full := seed(12, "<f@example.com>")

	if err := store.UpdateBodiesBatch([]BodyUpdate{
		{MessageID: trunc.ID, BodyText: "partial", Snippet: "partial", Truncated: true},
		{MessageID: full.ID, BodyText: "complete", Snippet: "complete"},
	}); err != nil {
		t.Fatalf("UpdateBodiesBatch: %v", err)
	}

	got, err := store.Get(trunc.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyTruncated {
		t.Fatal("batch: body_truncated not persisted for the truncated message")
	}
	got, err = store.Get(full.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BodyTruncated {
		t.Fatal("batch: body_truncated set for the complete message")
	}
}

func TestClearBodiesForFolder_ResetsTruncated(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 21,
		MessageID: "<c@example.com>", Subject: "Clear",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
	if err := store.UpdateBody(BodyUpdate{
		MessageID: m.ID, BodyText: "partial", Snippet: "partial", Truncated: true,
	}); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}

	if _, err := store.ClearBodiesForFolder(folderID); err != nil {
		t.Fatalf("ClearBodiesForFolder: %v", err)
	}
	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BodyTruncated {
		t.Fatal("body_truncated still set after ClearBodiesForFolder (Force Resync)")
	}
	if got.BodyFetched {
		t.Fatal("body_fetched still set after ClearBodiesForFolder")
	}
}
