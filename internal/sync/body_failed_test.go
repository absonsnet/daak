package sync

import (
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/message"
)

// TestShouldChargeFailure covers the decision boundary between persisting a
// body-parse failure (body_failed=1, message permanently skipped) and
// deferring to a future sync cycle when the response looks truncated.
//
// All sizes in bytes. maxMessageSize is the production constant — picking
// inputs around it directly verifies the "Daak-side cap is not a server
// truncation" branch.
func TestShouldChargeFailure(t *testing.T) {
	cases := []struct {
		name         string
		received     int64
		reported     int64
		wantedCharge bool
	}{
		{
			name:         "no reported size: charge (no signal to defer on)",
			received:     5_000,
			reported:     0,
			wantedCharge: true,
		},
		{
			name:         "received hit Daak's cap: charge (intentional truncation, retry won't help)",
			received:     maxMessageSize,
			reported:     maxMessageSize + 50_000_000,
			wantedCharge: true,
		},
		{
			name:         "received well below threshold: DEFER (likely server-side truncation)",
			received:     1_000,
			reported:     50_000,
			wantedCharge: false,
		},
		{
			name:         "received exactly at threshold (80%): charge (close enough; treat as real)",
			received:     8_000,
			reported:     10_000,
			wantedCharge: true,
		},
		{
			name:         "received just under threshold: DEFER",
			received:     7_999,
			reported:     10_000,
			wantedCharge: false,
		},
		{
			name:         "received equals reported: charge (full payload, empty parse is real)",
			received:     12_000,
			reported:     12_000,
			wantedCharge: true,
		},
		{
			name:         "received slightly over reported: charge (framing variation)",
			received:     12_010,
			reported:     12_000,
			wantedCharge: true,
		},
		{
			name:         "tiny message both received and reported: charge",
			received:     50,
			reported:     50,
			wantedCharge: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldChargeFailure(tc.received, tc.reported)
			if got != tc.wantedCharge {
				t.Errorf("shouldChargeFailure(received=%d, reported=%d) = %v, want %v",
					tc.received, tc.reported, got, tc.wantedCharge)
			}
		})
	}
}

// TestMarkUnresolvedAsFailed_AttachmentOnlyNotFlagged (audit finding 11):
// a message whose fetch produced attachments but no body text is complete,
// not failed — it must not be flagged body_failed. The empty-body control
// next to it proves flagging still works for genuinely unusable fetches.
func TestMarkUnresolvedAsFailed_AttachmentOnlyNotFlagged(t *testing.T) {
	t.Run("attachment-only update is resolved", func(t *testing.T) {
		engine, db, msgStore, _ := newReactionHandleTestEngine(t)
		seedHandleTestMessage(t, msgStore, "orig-1", "<att-1@example.com>", "", "alice@example.com", "Alice")

		engine.markUnresolvedAsFailed(
			[]string{"orig-1"},
			[]message.BodyUpdate{{MessageID: "orig-1", HasAttachments: true}},
			map[string]fetchedSize{"orig-1": {received: 5000, reported: 5000}},
			nil,
			nil,
		)

		var flag int
		if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "orig-1").Scan(&flag); err != nil {
			t.Fatalf("query body_failed: %v", err)
		}
		if flag != 0 {
			t.Errorf("body_failed = %d for attachment-only message, want 0", flag)
		}
	})

	t.Run("empty update is still flagged", func(t *testing.T) {
		engine, db, msgStore, _ := newReactionHandleTestEngine(t)
		seedHandleTestMessage(t, msgStore, "orig-1", "<empty-1@example.com>", "", "alice@example.com", "Alice")

		engine.markUnresolvedAsFailed(
			[]string{"orig-1"},
			[]message.BodyUpdate{{MessageID: "orig-1"}},
			map[string]fetchedSize{"orig-1": {received: 5000, reported: 5000}},
			nil,
			nil,
		)

		var flag int
		if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "orig-1").Scan(&flag); err != nil {
			t.Fatalf("query body_failed: %v", err)
		}
		if flag != 1 {
			t.Errorf("body_failed = %d for empty-body message, want 1 (flagging must still work)", flag)
		}
	})
}

// TestBatchUIDLookup_PlaceholderNeverMarkedBodyFailed (re-audit finding
// R5): a just-moved message carries a negative placeholder UID, which the
// batch lookup skips. The split must exclude it from the IDs passed to
// markUnresolvedAsFailed, so the move is never charged body_failed —
// charging it would strand the message, since every GetMessagesWithoutBody
// variant requires body_failed = 0 and only the destination sync can
// resolve the real UID. The real-UID control next to it proves flagging
// still works for IDs the server was actually asked for.
func TestBatchUIDLookup_PlaceholderNeverMarkedBodyFailed(t *testing.T) {
	engine, db, msgStore, _ := newReactionHandleTestEngine(t)

	if _, err := db.Exec(
		`INSERT INTO folders (id, account_id, name, path, folder_type)
		 VALUES ('folder-2', 'acc-1', 'Archive', 'Archive', 'archive')`,
	); err != nil {
		t.Fatalf("seed folder 2: %v", err)
	}

	seed := func(id string, uid uint32) {
		m := &message.Message{
			ID:        id,
			AccountID: "acc-1",
			FolderID:  "folder-1",
			UID:       uid,
			MessageID: "<" + id + "@example.com>",
			FromEmail: "alice@example.com",
			FromName:  "Alice",
			Date:      time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC),
			Subject:   "test",
		}
		if err := msgStore.Create(m); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	seed("real-1", 101)
	seed("moved-1", 102)

	// Move it: the row now carries the negative placeholder UID.
	if err := msgStore.MoveMessages([]string{"moved-1"}, "folder-2"); err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}

	// The batch lookup skips the placeholder UID.
	uidInfos, err := msgStore.GetMessageUIDsAndFolder([]string{"real-1", "moved-1"})
	if err != nil {
		t.Fatalf("GetMessageUIDsAndFolder: %v", err)
	}
	if len(uidInfos) != 1 {
		t.Fatalf("GetMessageUIDsAndFolder returned %d rows, want 1 (only real-1)", len(uidInfos))
	}

	// The split excludes the moved message from the requested set — the
	// exact set FetchBodiesInBackground passes to markUnresolvedAsFailed.
	fetchable, skipped := splitFetchableIDs([]string{"real-1", "moved-1"}, uidInfos)
	if len(fetchable) != 1 || fetchable[0] != "real-1" {
		t.Fatalf("splitFetchableIDs fetchable = %v, want [real-1]", fetchable)
	}
	if len(skipped) != 1 || skipped[0] != "moved-1" {
		t.Fatalf("splitFetchableIDs skipped = %v, want [moved-1]", skipped)
	}

	// Simulate the empty-batch outcome: the server returned nothing for
	// the requested IDs, so they are charged — but only the requested ones.
	engine.markUnresolvedAsFailed(fetchable, nil, nil, nil, nil)

	var flag int
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "real-1").Scan(&flag); err != nil {
		t.Fatalf("query body_failed real-1: %v", err)
	}
	if flag != 1 {
		t.Errorf("body_failed = %d for real-1, want 1 (flagging must still work for requested IDs)", flag)
	}
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "moved-1").Scan(&flag); err != nil {
		t.Fatalf("query body_failed moved-1: %v", err)
	}
	if flag != 0 {
		t.Errorf("body_failed = %d for moved-1, want 0 (placeholder UID must never be charged)", flag)
	}

	// And the moved message is still queued for a body fetch in its new
	// folder, so the body lands once the destination sync resolves the UID.
	ids, err := msgStore.GetMessagesWithoutBody("folder-2", 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (folder-2): %v", err)
	}
	found := false
	for _, id := range ids {
		if id == "moved-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("moved-1 not in body-fetch queue after the failed batch, got %v", ids)
	}
}

// TestBodyFailedReactionDetectedAfterRetry (audit finding 11, reaction half):
// an Outlook fallback reaction email that failed its first body fetch was
// permanently body-less, so its pill never appeared. After a Force Resync
// (ClearBodiesForFolder resets body_failed) the message is a fetch
// candidate again, and the re-fetch re-runs parseMessageBodyFull — where
// reaction detection lives — so the pill appears on the original.
func TestBodyFailedReactionDetectedAfterRetry(t *testing.T) {
	engine, db, msgStore, reactStore := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<orig@example.com>", "", "me@example.com", "Me")
	seedHandleTestMessage(t, msgStore, "react-1", "<outlook-react-1@smme.nust.edu.pk>", "<orig@example.com>", "sana.zulfiqar@smme.nust.edu.pk", "Dr Sana Zulfiqar")

	// The reaction email failed its first body fetch (transient timeout).
	if err := msgStore.MarkBodyFailed([]string{"react-1"}); err != nil {
		t.Fatalf("MarkBodyFailed: %v", err)
	}

	// Precondition: flagged rows are out of the body-fetch queue.
	ids, err := msgStore.GetMessagesWithoutBody("folder-1", 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (flagged): %v", err)
	}
	for _, id := range ids {
		if id == "react-1" {
			t.Fatalf("flagged reaction email is still queued for body fetch, want it excluded")
		}
	}

	// Force Resync step 1: clearing the folder's bodies must also clear
	// body_failed, or the re-sync would still skip the message.
	if _, err := msgStore.ClearBodiesForFolder("folder-1"); err != nil {
		t.Fatalf("ClearBodiesForFolder: %v", err)
	}
	var flag int
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "react-1").Scan(&flag); err != nil {
		t.Fatalf("query body_failed: %v", err)
	}
	if flag != 0 {
		t.Fatalf("body_failed = %d after ClearBodiesForFolder, want 0", flag)
	}
	ids, err = msgStore.GetMessagesWithoutBody("folder-1", 100, time.Time{})
	if err != nil {
		t.Fatalf("GetMessagesWithoutBody (post-clear): %v", err)
	}
	found := false
	for _, id := range ids {
		if id == "react-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reaction email not back in body-fetch queue after Force Resync, got %v", ids)
	}

	// The re-fetch re-parses the body, and detection runs inside the
	// parse — the Outlook fallback banner + quote block.
	raw := "From: Dr Sana Zulfiqar <sana.zulfiqar@smme.nust.edu.pk>\r\n" +
		"To: MEDD ASCEND <medd.ascend@gmail.com>\r\n" +
		"Subject: Re: Need Deliverables\r\n" +
		"Message-ID: <outlook-react-1@smme.nust.edu.pk>\r\n" +
		"In-Reply-To: <orig@example.com>\r\n" +
		"Content-Type: multipart/alternative; boundary=\"OUT\"\r\n" +
		"\r\n" +
		"--OUT\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" +
		"[thumbs up] Dr Sana Zulfiqar reacted to your message:\r\n" +
		"\r\n" +
		"From: MEDD ASCEND <medd.ascend@gmail.com>\r\n" +
		"Sent: Friday, 11 September 2026 10:42:23\r\n" +
		"To: Dr Sana Zulfiqar <sana.zulfiqar@smme.nust.edu.pk>\r\n" +
		"Subject: Re: Need Deliverables\r\n" +
		"\r\n" +
		"OK, Thanks, we'll be contact you on Monday.\r\n" +
		"--OUT\r\n" +
		"Content-Type: text/html; charset=\"utf-8\"\r\n" +
		"\r\n" +
		"<html><body><p>[thumbs up] Dr Sana Zulfiqar reacted to your message:</p></body></html>\r\n" +
		"--OUT--\r\n"

	parsed := engine.parseMessageBodyInternal([]byte(raw), "react-1")
	if parsed.Reaction == nil {
		t.Fatal("Outlook fallback after retry: Reaction = nil, want detected reaction")
	}
	if parsed.Reaction.Emoji != "👍" {
		t.Fatalf("Outlook fallback after retry: Emoji = %q, want 👍", parsed.Reaction.Emoji)
	}
	if !engine.handleReaction("react-1", parsed.Reaction) {
		t.Fatal("Outlook fallback after retry: handleReaction returned false, want linked")
	}
	listed, err := reactStore.ListByMessage("orig-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Emoji != "👍" || listed[0].ReactorEmail != "sana.zulfiqar@smme.nust.edu.pk" {
		t.Fatalf("ListByMessage = %+v, want 1 👍 reaction from sana.zulfiqar@smme.nust.edu.pk", listed)
	}
	if !isHidden(t, db, "react-1") {
		t.Fatal("reaction email is not hidden after retry, want is_hidden=1")
	}
}

// Audit4 findings 22 + 23: a timed-out parse and an empty fetch must
// never charge body_failed — both are deferred so the next sync retries.
func TestMarkUnresolvedAsFailed_DeferredIDsNotFlagged(t *testing.T) {
	engine, db, msgStore, _ := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<defer-1@example.com>", "", "alice@example.com", "Alice")
	seedHandleTestMessage(t, msgStore, "orig-2", "<defer-2@example.com>", "", "alice@example.com", "Alice")

	engine.markUnresolvedAsFailed(
		[]string{"orig-1", "orig-2"},
		[]message.BodyUpdate{{MessageID: "orig-1"}, {MessageID: "orig-2"}},
		map[string]fetchedSize{
			"orig-1": {received: 5000, reported: 5000},
			"orig-2": {received: 0, reported: 5000},
		},
		nil,
		map[string]bool{"orig-1": true, "orig-2": true},
	)

	for _, id := range []string{"orig-1", "orig-2"} {
		var flag int
		if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, id).Scan(&flag); err != nil {
			t.Fatalf("query body_failed %s: %v", id, err)
		}
		if flag != 0 {
			t.Errorf("body_failed = %d for deferred %s, want 0", flag, id)
		}
	}
}

// Audit4 finding 22: a timed-out parse defers the body update even
// when the fallback produced partial text — the partial body must
// never be stored as complete.
func TestDeferBodyUpdate_TimedOutParse(t *testing.T) {
	pb := &ProcessedBody{MessageID: "msg-1", BodyText: "partial fallback", ParseTimedOut: true, ReceivedBytes: 5000, ReportedSize: 5000}
	if !deferBodyUpdate(pb) {
		t.Fatalf("deferBodyUpdate(timed-out parse) = false, want true")
	}
}
