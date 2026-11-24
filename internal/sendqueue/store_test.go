package sendqueue

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/database"
)

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func testItem(id string) Item {
	now := time.Now().UTC().Truncate(time.Second)
	return Item{
		ID:          id,
		AccountID:   "acc-1",
		DraftID:     "draft-1",
		MessageJSON: `{"subject":"hi"}`,
		Subject:     "hi",
		Recipient:   "a@example.com",
		FromAddress: "me@example.com",
		To:          []string{"a@example.com"},
		Status:      StatusPending,
		Attempts:    0,
		MaxAttempts: DefaultMaxAttempts,
		SendAt:      now.Add(5 * time.Second),
		DueAt:       now.Add(5 * time.Second),
		CreatedAt:   now,
	}
}

func TestEnqueueGetList(t *testing.T) {
	s := NewStore(openTestDB(t))
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("q1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("Get returned nil for an enqueued item")
	}
	if got.Subject != "hi" || got.Recipient != "a@example.com" || got.DraftID != "draft-1" {
		t.Fatalf("Get = %+v, want subject hi / recipient a@example.com / draft-1", got)
	}
	if got.MessageJSON != "" {
		t.Fatalf("Get must not load message JSON, got %q", got.MessageJSON)
	}
	full, err := s.GetForSend("q1")
	if err != nil {
		t.Fatal(err)
	}
	if full == nil || full.MessageJSON != `{"subject":"hi"}` {
		t.Fatalf("GetForSend message JSON = %q, want stored JSON", full.MessageJSON)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "q1" {
		t.Fatalf("List = %d items, want 1 with id q1", len(list))
	}
	missing, err := s.Get("nope")
	if err != nil || missing != nil {
		t.Fatalf("Get(missing) = %v, %v, want nil, nil", missing, err)
	}
}

func TestListDue(t *testing.T) {
	s := NewStore(openTestDB(t))
	now := time.Now().UTC()
	early := testItem("early")
	early.SendAt = now.Add(-time.Minute)
	early.DueAt = now.Add(-time.Minute)
	late := testItem("late")
	late.SendAt = now.Add(time.Hour)
	late.DueAt = now.Add(time.Hour)
	for _, it := range []Item{late, early} {
		if err := s.Enqueue(it); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.ListDue(now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "early" {
		t.Fatalf("ListDue = %v, want only [early]", due)
	}
}

func TestClaimIsAtomic(t *testing.T) {
	s := NewStore(openTestDB(t))
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, err := s.Claim("q1", now)
	if err != nil || !claimed {
		t.Fatalf("first Claim = %v, %v, want true, nil", claimed, err)
	}
	// Second claim must fail: the item is 'sending', not 'pending'.
	claimed, err = s.Claim("q1", now)
	if err != nil || claimed {
		t.Fatalf("second Claim = %v, %v, want false, nil", claimed, err)
	}
}

func TestDeletePendingOnlyPending(t *testing.T) {
	s := NewStore(openTestDB(t))
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	ok, err := s.DeletePending("q1")
	if err != nil || !ok {
		t.Fatalf("DeletePending(pending) = %v, %v, want true, nil", ok, err)
	}
	// Already gone: a second delete is a no-op.
	ok, err = s.DeletePending("q1")
	if err != nil || ok {
		t.Fatalf("DeletePending(deleted) = %v, %v, want false, nil", ok, err)
	}
	// A claimed (sending) item cannot be undone — the dispatch is
	// already running — but Delete still removes it outright (the
	// post-send path, after a successful send).
	if err := s.Enqueue(testItem("q2")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.Claim("q2", now); err != nil {
		t.Fatal(err)
	}
	ok, err = s.DeletePending("q2")
	if err != nil || ok {
		t.Fatalf("DeletePending(sending) = %v, %v, want false, nil", ok, err)
	}
	ok, err = s.Delete("q2")
	if err != nil || !ok {
		t.Fatalf("Delete(sending) = %v, %v, want true, nil", ok, err)
	}
	if got, err := s.Get("q2"); err != nil || got != nil {
		t.Fatalf("Get(deleted) = %+v, %v, want nil, nil", got, err)
	}
}

func TestReactionItemRoundTrip(t *testing.T) {
	s := NewStore(openTestDB(t))
	raw := []byte("From: me@example.com\r\n\r\nbody")
	item := testItem("r1")
	item.Kind = KindReaction
	item.MessageJSON = ""
	item.RawMessage = raw
	item.ReactionMessageID = "msg-1"
	item.DraftID = ""
	if err := s.Enqueue(item); err != nil {
		t.Fatal(err)
	}
	// The list view carries the kind and the reacted-to message id,
	// but never the pre-built bytes.
	got, err := s.Get("r1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Kind != KindReaction || got.ReactionMessageID != "msg-1" {
		t.Fatalf("Get(reaction) = %+v, want kind reaction / msg-1", got)
	}
	if len(got.RawMessage) != 0 {
		t.Fatalf("Get(reaction) returned %d raw bytes, want none in the list view", len(got.RawMessage))
	}
	// Dispatch loads the bytes.
	full, err := s.GetForSend("r1")
	if err != nil {
		t.Fatal(err)
	}
	if full == nil || string(full.RawMessage) != string(raw) {
		t.Fatalf("GetForSend(reaction) raw = %q, want the queued bytes", full.RawMessage)
	}
}

func TestRetryOnlyFailed(t *testing.T) {
	s := NewStore(openTestDB(t))
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Pending items cannot be "retried".
	ok, err := s.Retry("q1", now)
	if err != nil || ok {
		t.Fatalf("Retry(pending) = %v, %v, want false, nil", ok, err)
	}
	// Fail it, then retry: attempts reset, due now.
	if err := s.RecordAttempt("q1", DefaultMaxAttempts, StatusFailed, now, "boom"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.Retry("q1", now)
	if err != nil || !ok {
		t.Fatalf("Retry(failed) = %v, %v, want true, nil", ok, err)
	}
	got, _ := s.Get("q1")
	if got.Status != StatusPending || got.Attempts != 0 || got.LastError != "" {
		t.Fatalf("after Retry: %+v, want pending/0 attempts/no error", got)
	}
	if got.DueAt.Before(now.Add(-time.Second)) || got.DueAt.After(now.Add(time.Second)) {
		t.Fatalf("after Retry: dueAt %v, want ~now", got.DueAt)
	}
}

func TestResetStaleSending(t *testing.T) {
	s := NewStore(openTestDB(t))
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.Claim("q1", now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResetStaleSending(now.Add(-10 * time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("ResetStaleSending = %d, %v, want 1, nil", n, err)
	}
	got, _ := s.Get("q1")
	if got.Status != StatusPending {
		t.Fatalf("after reset: status %q, want pending", got.Status)
	}
	// A fresh claim is NOT stale.
	if _, err := s.Claim("q1", now); err != nil {
		t.Fatal(err)
	}
	n, err = s.ResetStaleSending(now.Add(-10 * time.Minute))
	if err != nil || n != 0 {
		t.Fatalf("ResetStaleSending(fresh) = %d, %v, want 0, nil", n, err)
	}
}

func TestStatsAndCountPending(t *testing.T) {
	s := NewStore(openTestDB(t))
	for _, id := range []string{"q1", "q2"} {
		if err := s.Enqueue(testItem(id)); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.CountPending(); err != nil || n != 2 {
		t.Fatalf("CountPending = %d, %v, want 2, nil", n, err)
	}
	now := time.Now().UTC()
	if _, err := s.Claim("q1", now); err != nil {
		t.Fatal(err)
	}
	// A sent row is deleted, not kept: the Outbox holds no history.
	if ok, err := s.Delete("q1"); err != nil || !ok {
		t.Fatalf("Delete(sent) = %v, %v, want true, nil", ok, err)
	}
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 || st.Pending != 1 || st.Sent != 0 {
		t.Fatalf("Stats = %+v, want total 1 / pending 1 / sent 0", st)
	}
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, time.Minute},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{10, time.Hour}, // capped
	}
	for _, c := range cases {
		if got := Backoff(c.attempts); got != c.want {
			t.Errorf("Backoff(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}
}

func TestIsPermanentError(t *testing.T) {
	permanent := []string{
		"failed to login to SMTP server: 535 Authentication failed",
		"account \"x\" is configured as receive-only (no outgoing server)",
		"no recipients specified",
		"failed to build message: bad header",
		"account not found: abc",
		"OAuth token refresh failed, re-authorization required: unauthorized_client",
	}
	for _, msg := range permanent {
		if !IsPermanentError(errString(msg)) {
			t.Errorf("IsPermanentError(%q) = false, want true", msg)
		}
	}
	transient := []string{
		"failed to connect to SMTP server: connection refused",
		"failed to send message: read tcp: i/o timeout",
		"server busy, try again later",
		"451 greylisted, try again",
		"EOF",
	}
	for _, msg := range transient {
		if IsPermanentError(errString(msg)) {
			t.Errorf("IsPermanentError(%q) = true, want false", msg)
		}
	}
	if IsPermanentError(nil) {
		t.Errorf("IsPermanentError(nil) = true, want false")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestHasActiveForDraft: a draft is "active" while its queue row is
// pending or sending, and stops being active once the row is gone
// (undone/sent) or failed — so QueueSend can reject a second queue
// attempt for the same draft during the undo window.
func TestHasActiveForDraft(t *testing.T) {
	s := NewStore(openTestDB(t))
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || active {
		t.Fatalf("HasActiveForDraft(empty) = %v, %v, want false, nil", active, err)
	}
	if active, err := s.HasActiveForDraft(""); err != nil || active {
		t.Fatalf("HasActiveForDraft(\"\") = %v, %v, want false, nil", active, err)
	}
	if err := s.Enqueue(testItem("q1")); err != nil {
		t.Fatal(err)
	}
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || !active {
		t.Fatalf("HasActiveForDraft(pending) = %v, %v, want true, nil", active, err)
	}
	if active, err := s.HasActiveForDraft("other-draft"); err != nil || active {
		t.Fatalf("HasActiveForDraft(other) = %v, %v, want false, nil", active, err)
	}
	// Claimed (sending) still counts as active.
	now := time.Now().UTC()
	if ok, err := s.Claim("q1", now); err != nil || !ok {
		t.Fatalf("Claim = %v, %v, want true, nil", ok, err)
	}
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || !active {
		t.Fatalf("HasActiveForDraft(sending) = %v, %v, want true, nil", active, err)
	}
	// Failed rows do not block a fresh queue attempt.
	if err := s.RecordAttempt("q1", 1, StatusFailed, now, "boom"); err != nil {
		t.Fatal(err)
	}
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || active {
		t.Fatalf("HasActiveForDraft(failed) = %v, %v, want false, nil", active, err)
	}
	// Neither does a deleted (undone/sent) row.
	if err := s.Enqueue(testItem("q2")); err != nil {
		t.Fatal(err)
	}
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || !active {
		t.Fatalf("HasActiveForDraft(re-queued) = %v, %v, want true, nil", active, err)
	}
	if ok, err := s.DeletePending("q2"); err != nil || !ok {
		t.Fatalf("DeletePending = %v, %v, want true, nil", ok, err)
	}
	if active, err := s.HasActiveForDraft("draft-1"); err != nil || active {
		t.Fatalf("HasActiveForDraft(deleted) = %v, %v, want false, nil", active, err)
	}
}
