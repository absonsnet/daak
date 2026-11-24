package message

import (
	"testing"
	"time"
)

// TestMarkHidden_HidesFromLists confirms the is_hidden flag (migration v45)
// actually drops a message from every user-visible list/count surface, and
// from the body-fetch queue — the whole point of hiding consumed reaction
// emails instead of deleting them (a delete would make the next sync
// re-fetch the UID as new).
func TestMarkHidden_HidesFromLists(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	visible := &Message{
		ID: "visible", AccountID: accountID, FolderID: folderID,
		UID: 1, Subject: "Hello", Date: now,
	}
	hidden := &Message{
		ID: "hidden", AccountID: accountID, FolderID: folderID,
		UID: 2, Subject: "👍", Date: now,
	}
	for _, m := range []*Message{visible, hidden} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	// Sanity: both listed before hiding.
	headers, err := s.ListByFolder(folderID, 0, 10)
	if err != nil {
		t.Fatalf("ListByFolder (pre-hide): %v", err)
	}
	if len(headers) != 2 {
		t.Fatalf("pre-hide: ListByFolder returned %d, want 2", len(headers))
	}
	if n, err := s.CountByFolder(folderID); err != nil || n != 2 {
		t.Fatalf("pre-hide: CountByFolder = %d, err = %v, want 2", n, err)
	}
	if n, err := s.CountUnreadByFolder(folderID); err != nil || n != 2 {
		t.Fatalf("pre-hide: CountUnreadByFolder = %d, err = %v, want 2", n, err)
	}

	if err := s.MarkHidden("hidden"); err != nil {
		t.Fatalf("MarkHidden: %v", err)
	}

	// List, counts, and conversation list must only see the visible message.
	headers, err = s.ListByFolder(folderID, 0, 10)
	if err != nil {
		t.Fatalf("ListByFolder (post-hide): %v", err)
	}
	if len(headers) != 1 || headers[0].ID != "visible" {
		t.Fatalf("post-hide: ListByFolder = %v, want only [visible]", headers)
	}
	if n, err := s.CountByFolder(folderID); err != nil || n != 1 {
		t.Errorf("post-hide: CountByFolder = %d, err = %v, want 1", n, err)
	}
	if n, err := s.CountUnreadByFolder(folderID); err != nil || n != 1 {
		t.Errorf("post-hide: CountUnreadByFolder = %d, err = %v, want 1 (only the visible message is unread)", n, err)
	}
	convs, err := s.ListConversationsByFolder(folderID, 0, 10, "newest", "", "", "")
	if err != nil {
		t.Fatalf("ListConversationsByFolder (post-hide): %v", err)
	}
	if len(convs) != 1 {
		t.Errorf("post-hide: ListConversationsByFolder returned %d, want 1", len(convs))
	}
	if n, err := s.CountConversationsByFolder(folderID, "", "", ""); err != nil || n != 1 {
		t.Errorf("post-hide: CountConversationsByFolder = %d, err = %v, want 1", n, err)
	}
	ids, err := s.GetAllIDsByFolder(folderID)
	if err != nil {
		t.Fatalf("GetAllIDsByFolder (post-hide): %v", err)
	}
	if len(ids) != 1 || ids[0] != "visible" {
		t.Errorf("post-hide: GetAllIDsByFolder = %v, want [visible]", ids)
	}

	// The hidden row must also leave the body-fetch queue: it was marked
	// body_fetched=1 by MarkHidden, and the is_hidden guard excludes it
	// even if its body is empty (the self-healing branch would otherwise
	// re-queue it forever).
	queued, err := s.GetMessagesWithoutBodyAndSize(folderID, 10, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBodyAndSize (post-hide): %v", err)
	}
	for _, q := range queued {
		if q.ID == "hidden" {
			t.Errorf("post-hide: hidden message still in body-fetch queue: %v", queued)
		}
	}

	// The row itself is still there (Get by id works, and the UID is still
	// known to sync) — hiding is a flag, not a delete.
	got, err := s.Get("hidden")
	if err != nil {
		t.Fatalf("Get(hidden): %v", err)
	}
	if got == nil {
		t.Fatal("Get(hidden) returned nil, want the row to still exist")
	}
	if !got.BodyFetched {
		t.Error("Get(hidden).BodyFetched = false, want true (MarkHidden sets it)")
	}
	if !got.IsRead {
		t.Error("Get(hidden).IsRead = false, want true (MarkHidden sets it)")
	}
}

// TestGetInternalIDByMessageID matches Message-IDs with or without angle
// brackets, as sent in In-Reply-To vs stored from the envelope.
func TestGetInternalIDByMessageID(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)

	m := &Message{
		ID: "m1", AccountID: accountID, FolderID: folderID,
		UID: 1, MessageID: "<orig@example.com>", Date: time.Now().UTC(),
	}
	if err := s.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, query := range []string{"<orig@example.com>", "orig@example.com"} {
		got, err := s.GetInternalIDByMessageID(accountID, query)
		if err != nil {
			t.Fatalf("GetInternalIDByMessageID(%q) error = %v", query, err)
		}
		if got != "m1" {
			t.Errorf("GetInternalIDByMessageID(%q) = %q, want %q", query, got, "m1")
		}
	}

	if _, err := s.GetInternalIDByMessageID(accountID, "<missing@example.com>"); err == nil {
		t.Error("GetInternalIDByMessageID(missing) error = nil, want an error")
	}
}
