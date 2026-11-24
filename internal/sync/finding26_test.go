package sync

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// Audit finding 26: truncation at the 10 MB part / 50 MB message caps was
// only a log line — the partial body was stored with body_fetched=1, so
// it looked complete and was never retried, and attachment sizes were
// understated at exactly 10 MB. The fix records a truncation flag
// (body_truncated, persisted from ParsedBody.Truncated via
// ProcessedBody/BodyUpdate) and records an attachment's true decoded
// size. These tests pin the flag at the parse layer and the size at the
// attachment layer.

func TestIsBodyTruncated(t *testing.T) {
	// Past the 50 MB cap: truncated, however the parser did.
	if !isBodyTruncated(maxMessageSize+1, false) {
		t.Fatal("received maxMessageSize+1 must be truncated")
	}
	// Exactly at the cap: complete — the +1 read distinguishes this.
	if isBodyTruncated(maxMessageSize, false) {
		t.Fatal("received exactly maxMessageSize must not be truncated")
	}
	if isBodyTruncated(maxMessageSize-1, false) {
		t.Fatal("received below the cap must not be truncated")
	}
	// A part over its own 10 MB cap flags the message even when the
	// whole message is far below 50 MB.
	if !isBodyTruncated(1024, true) {
		t.Fatal("parsedTruncated must flag the message regardless of total size")
	}
}

func overCapTextRaw(t *testing.T, size int64) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Big\r\nMessage-ID: <big@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"MIXED\"\r\n\r\n")
	b.WriteString("--MIXED\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.Repeat("a", int(size)))
	b.WriteString("\r\n--MIXED--\r\n")
	return b.String()
}

func TestParseMessageBodyInternal_PartOverCapFlaggedTruncated(t *testing.T) {
	parsed := testEngine().parseMessageBodyInternal([]byte(overCapTextRaw(t, maxPartSize+100)), "msg-1")
	if !parsed.Truncated {
		t.Fatal("Truncated=false for a text part 100 bytes over the 10 MB cap")
	}
	if int64(len(parsed.BodyText)) != maxPartSize {
		t.Fatalf("BodyText len=%d, want exactly maxPartSize (%d): only the first part is stored", len(parsed.BodyText), maxPartSize)
	}
}

func TestParseMessageBodyInternal_PartUnderCapNotTruncated(t *testing.T) {
	parsed := testEngine().parseMessageBodyInternal([]byte(overCapTextRaw(t, 1024)), "msg-1")
	if parsed.Truncated {
		t.Fatal("Truncated=true for a 1 KB text part")
	}
	if len(parsed.BodyText) != 1024 {
		t.Fatalf("BodyText len=%d, want 1024", len(parsed.BodyText))
	}
}

func TestParseMessageBodyInternal_SinglePartOverCapFlaggedTruncated(t *testing.T) {
	raw := "From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Big\r\nMessage-ID: <big1@example.com>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.Repeat("b", int(maxPartSize+1))
	parsed := testEngine().parseMessageBodyInternal([]byte(raw), "msg-1")
	if !parsed.Truncated {
		t.Fatal("Truncated=false for a single-part body over the 10 MB cap")
	}
	if int64(len(parsed.BodyText)) != maxPartSize {
		t.Fatalf("BodyText len=%d, want exactly maxPartSize (%d)", len(parsed.BodyText), maxPartSize)
	}
}

func TestParseMessageBodyInternal_AttachmentSizeNotUnderstated(t *testing.T) {
	// Decoded attachment content 1 KB over the old 10 MB read cap: the
	// old code recorded Size == maxPartSize; the true size must be kept.
	decoded := bytes.Repeat([]byte("B"), int(maxPartSize)+1024)
	encoded := base64.StdEncoding.EncodeToString(decoded)
	var b strings.Builder
	b.WriteString("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Big file\r\nMessage-ID: <bigfile@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"MIXED\"\r\n\r\n")
	b.WriteString("--MIXED\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee attached.\r\n")
	b.WriteString("--MIXED\r\nContent-Type: application/octet-stream; name=\"big.bin\"\r\nContent-Disposition: attachment; filename=\"big.bin\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + encoded + "\r\n")
	b.WriteString("--MIXED--\r\n")

	parsed := testEngine().parseMessageBodyInternal([]byte(b.String()), "msg-1")
	if len(parsed.Attachments) != 1 {
		t.Fatalf("attachments=%d, want 1", len(parsed.Attachments))
	}
	att := parsed.Attachments[0]
	if att.Size != len(decoded) {
		t.Fatalf("attachment Size=%d, want true decoded size %d (not the 10 MB read cap)", att.Size, len(decoded))
	}
	// Nothing stored was cut: the attachment content is fetched
	// on-demand from the server, so the message itself is not truncated.
	if parsed.Truncated {
		t.Fatal("Truncated=true for an over-cap attachment whose metadata is complete")
	}
}

func TestBodyUpdateFromProcessedBody_CarriesTruncated(t *testing.T) {
	bu := bodyUpdateFromProcessedBody(&ProcessedBody{MessageID: "m-1", BodyText: "partial", Truncated: true})
	if !bu.Truncated {
		t.Fatal("BodyUpdate.Truncated=false for a truncated ProcessedBody")
	}
	bu = bodyUpdateFromProcessedBody(&ProcessedBody{MessageID: "m-1", BodyText: "full"})
	if bu.Truncated {
		t.Fatal("BodyUpdate.Truncated=true for a complete ProcessedBody")
	}
}
