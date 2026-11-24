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
