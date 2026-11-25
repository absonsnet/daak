package sync

import (
	"testing"

	"github.com/absonsnet/daak/internal/message"
)

// Re-audit finding R10: a message with attachments but no body text was
// treated as complete forever. A fetch that extracts at least one
// attachment but yields no body text is "resolved" (attachments count as
// usable content), so no body_failed flag is set and background sync never
// retries it. That is correct for genuinely attachment-only mail, but a
// transient failure on the text parts — a part read error, or a
// server-truncated fetch that still yielded attachments — loses the body
// text silently. The fix records at parse time whether a text part
// existed but could not be read (ParsedBody.TextPartsFailed), and the
// flag/defer decision then follows the fetch's size signal.

// --- parse time: the failure is recorded, genuine att-only is not ---

const r10MultipartBadTextPart = "From: a@example.com\r\n" +
	"To: b@example.com\r\n" +
	"Subject: probe\r\n" +
	"Content-Type: multipart/mixed; boundary=\"B\"\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"!!!not-valid-base64!!!\r\n" +
	"--B\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"aGVsbG8=\r\n" +
	"--B--\r\n"

const r10MultipartGenuineAttOnly = "From: a@example.com\r\n" +
	"To: b@example.com\r\n" +
	"Subject: probe\r\n" +
	"Content-Type: multipart/mixed; boundary=\"B\"\r\n" +
	"\r\n" +
	"--B\r\n" +
	"Content-Type: application/pdf\r\n" +
	"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"aGVsbG8=\r\n" +
	"--B--\r\n"

func TestParseMessageBody_TextPartReadErrorRecorded(t *testing.T) {
	e := &Engine{}
	parsed := e.parseMessageBodyInternal([]byte(r10MultipartBadTextPart), "r10-1")
	if parsed.BodyText != "" || parsed.BodyHTML != "" {
		t.Fatalf("BodyText=%q BodyHTML=%q, want both empty (corrupt text part yields nothing)", parsed.BodyText, parsed.BodyHTML)
	}
	if !parsed.HasAttachments || len(parsed.Attachments) != 1 {
		t.Fatalf("HasAttachments=%v nAtt=%d, want true/1 (the PDF still extracts)", parsed.HasAttachments, len(parsed.Attachments))
	}
	if !parsed.TextPartsFailed {
		t.Fatal("TextPartsFailed = false, want true (the text part existed but could not be read)")
	}
}

func TestParseMessageBody_GenuineAttachmentOnlyNotFailed(t *testing.T) {
	e := &Engine{}
	parsed := e.parseMessageBodyInternal([]byte(r10MultipartGenuineAttOnly), "r10-2")
	if parsed.BodyText != "" || parsed.BodyHTML != "" {
		t.Fatalf("BodyText=%q BodyHTML=%q, want both empty", parsed.BodyText, parsed.BodyHTML)
	}
	if !parsed.HasAttachments || len(parsed.Attachments) != 1 {
		t.Fatalf("HasAttachments=%v nAtt=%d, want true/1", parsed.HasAttachments, len(parsed.Attachments))
	}
	if parsed.TextPartsFailed {
		t.Fatal("TextPartsFailed = true, want false (no text parts exist — genuinely attachment-only)")
	}
}

func TestParseMessageBody_SinglePartReadErrorRecorded(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: probe\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"!!!not-valid-base64!!!\r\n"
	e := &Engine{}
	parsed := e.parseMessageBodyInternal([]byte(raw), "r10-3")
	if parsed.BodyText != "" {
		t.Fatalf("BodyText=%q, want empty", parsed.BodyText)
	}
	if !parsed.TextPartsFailed {
		t.Fatal("TextPartsFailed = false, want true (single-part text body unreadable)")
	}
}

func TestParseMessageBody_TextRecoveredFromSiblingPartIsResolved(t *testing.T) {
	// One text part fails, the sibling plain part succeeds: body text
	// exists, so the message is resolved even though a part failed.
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: probe\r\n" +
		"Content-Type: multipart/mixed; boundary=\"B\"\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/html; charset=\"utf-8\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"!!!not-valid-base64!!!\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=\"utf-8\"\r\n" +
		"\r\n" +
		"hello from the plain part\r\n" +
		"--B--\r\n"
	e := &Engine{}
	parsed := e.parseMessageBodyInternal([]byte(raw), "r10-4")
	if parsed.BodyText == "" {
		t.Fatal("BodyText empty, want the surviving plain part's text")
	}
	if !parsed.TextPartsFailed {
		t.Fatal("TextPartsFailed = false, want true (the HTML part did fail)")
	}
	// ... but the update is still usable: text was recovered.
	if bu := bodyUpdateFromProcessedBody(&ProcessedBody{MessageID: "r10-4", BodyText: parsed.BodyText, TextPartsFailed: parsed.TextPartsFailed}); !bu.HasUsableContent() {
		t.Fatal("HasUsableContent = false, want true (recovered text resolves the message)")
	}
}

// --- markUnresolvedAsFailed: flag the text failure, defer truncation ---

func TestMarkUnresolvedAsFailed_TextPartFailureWithAttachmentsFlagged(t *testing.T) {
	engine, db, msgStore, _ := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<r10-flag@example.com>", "", "alice@example.com", "Alice")

	// Full-size fetch: the text part failed to read, attachments came
	// out. Before R10 this was "resolved" and never flagged.
	engine.markUnresolvedAsFailed(
		[]string{"orig-1"},
		[]message.BodyUpdate{{MessageID: "orig-1", HasAttachments: true, TextPartsFailed: true}},
		map[string]fetchedSize{"orig-1": {received: 5000, reported: 5000}},
		nil,
		nil,
	)

	var flag int
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "orig-1").Scan(&flag); err != nil {
		t.Fatalf("query body_failed: %v", err)
	}
	if flag != 1 {
		t.Errorf("body_failed = %d, want 1 (text-part failure with attachments must be flagged, not treated as complete)", flag)
	}
}

func TestMarkUnresolvedAsFailed_TextPartFailureTruncatedDeferred(t *testing.T) {
	engine, db, msgStore, _ := newReactionHandleTestEngine(t)
	seedHandleTestMessage(t, msgStore, "orig-1", "<r10-defer@example.com>", "", "alice@example.com", "Alice")

	// Truncated fetch: only 1000 of 50000 bytes arrived. The text parts
	// may simply have been cut off — defer, don't flag, so the next sync
	// retries (deferBodyUpdate also withholds the BodyUpdate, so
	// body_fetched stays 0 and the message stays queued).
	engine.markUnresolvedAsFailed(
		[]string{"orig-1"},
		[]message.BodyUpdate{{MessageID: "orig-1", HasAttachments: true, TextPartsFailed: true}},
		map[string]fetchedSize{"orig-1": {received: 1000, reported: 50000}},
		nil,
		nil,
	)

	var flag int
	if err := db.QueryRow(`SELECT body_failed FROM messages WHERE id = ?`, "orig-1").Scan(&flag); err != nil {
		t.Fatalf("query body_failed: %v", err)
	}
	if flag != 0 {
		t.Errorf("body_failed = %d, want 0 (truncated fetch defers to a retry, it is not charged)", flag)
	}
}

// --- deferBodyUpdate: the persist gate for truncated att-only fetches ---

func TestDeferBodyUpdate(t *testing.T) {
	cases := []struct {
		name string
		pb   *ProcessedBody
		want bool
	}{
		{
			name: "genuinely attachment-only, full fetch: persist",
			pb:   &ProcessedBody{HasAttachments: true, ReportedSize: 5000, ReceivedBytes: 5000},
			want: false,
		},
		{
			name: "attachment-only, truncated fetch: defer",
			pb:   &ProcessedBody{HasAttachments: true, ReportedSize: 50000, ReceivedBytes: 1000},
			want: true,
		},
		{
			name: "attachment-only with failed text part, truncated fetch: defer",
			pb:   &ProcessedBody{HasAttachments: true, TextPartsFailed: true, ReportedSize: 50000, ReceivedBytes: 1000},
			want: true,
		},
		{
			name: "attachment-only with failed text part, full fetch: persist (flag path, not defer)",
			pb:   &ProcessedBody{HasAttachments: true, TextPartsFailed: true, ReportedSize: 5000, ReceivedBytes: 5000},
			want: false,
		},
		{
			name: "body text present, truncated fetch: persist (text was recovered)",
			pb:   &ProcessedBody{BodyText: "hello", HasAttachments: true, ReportedSize: 50000, ReceivedBytes: 1000},
			want: false,
		},
		{
			name: "empty, no attachments, truncated fetch: persist decision is not this helper's (no update withheld for a no-attachment fetch)",
			pb:   &ProcessedBody{ReportedSize: 50000, ReceivedBytes: 1000},
			want: false,
		},
		{
			name: "reaction: never deferred",
			pb:   &ProcessedBody{IsReaction: true, HasAttachments: true, ReportedSize: 50000, ReceivedBytes: 1000},
			want: false,
		},
		{
			name: "nil: never deferred",
			pb:   nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deferBodyUpdate(tc.pb); got != tc.want {
				t.Errorf("deferBodyUpdate() = %v, want %v", got, tc.want)
			}
		})
	}
}
