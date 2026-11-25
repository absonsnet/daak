package sync

import (
	"strings"
	"testing"
)

// Re-audit finding R19: encrypted mail opened from server search gained a
// phantom attachment. The search path's legacy parser classified the
// encrypted parts as attachments (the modern parser returns early for
// encrypted mail and never does), and the extractor did the same, so the
// message showed a paperclip and a downloadable file — usually
// attachment.bin — that was actually the encrypted message itself.
// These tests pin the legacy path (parseMessageBody, the one
// buildMessageFromStreamedData uses) and the extractor-facing
// classification: encrypted mail yields zero attachments on every shape,
// while genuinely attached files are still reported.

const r19NestedEncryptedRaw = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Mixed with encrypted part\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MIXED\"\r\n" +
	"\r\n" +
	"--MIXED\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"See the encrypted part below.\r\n" +
	"--MIXED\r\n" +
	"Content-Type: multipart/encrypted; boundary=\"PGP\"; protocol=\"application/pgp-encrypted\"\r\n" +
	"\r\n" +
	"--PGP\r\n" +
	"Content-Type: application/pgp-encrypted\r\n" +
	"\r\n" +
	"Version: 1\r\n" +
	"\r\n" +
	"--PGP\r\n" +
	"Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\n" +
	"Content-Disposition: attachment; filename=\"encrypted.asc\"\r\n" +
	"\r\n" +
	"-----BEGIN PGP MESSAGE-----\r\n" +
	"wcBMA1234567890abcdef\r\n" +
	"-----END PGP MESSAGE-----\r\n" +
	"\r\n" +
	"--PGP--\r\n" +
	"--MIXED--\r\n"

func TestParseMessageBody_EncryptedNoAttachments(t *testing.T) {
	for name, raw := range map[string]string{
		"smime": smimeEncryptedRaw,
		"pgp":   pgpEncryptedRaw,
	} {
		bodyText, bodyHTML, hasAttachments, _ := testEngine().parseMessageBody([]byte(raw))
		if hasAttachments {
			t.Fatalf("[%s] hasAttachments = true, want false for encrypted mail", name)
		}
		if bodyText != "" || bodyHTML != "" {
			t.Fatalf("[%s] body text/html should stay empty until decrypted: text=%q html=%q", name, bodyText, bodyHTML)
		}
		// The modern parser must agree: same message, same answer.
		parsed := testEngine().parseMessageBodyInternal([]byte(raw), "r19-"+name)
		if parsed.HasAttachments || len(parsed.Attachments) != 0 {
			t.Fatalf("[%s] modern parser reports attachments=%d, want 0", name, len(parsed.Attachments))
		}
	}
}

func TestParseMessageBody_NestedEncryptedNoAttachments(t *testing.T) {
	bodyText, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(r19NestedEncryptedRaw))
	if hasAttachments {
		t.Fatal("hasAttachments = true, want false (the only non-text part is an encrypted subtree)")
	}
	if !strings.Contains(bodyText, "See the encrypted part below.") {
		t.Fatalf("bodyText = %q, want the plain-text part preserved", bodyText)
	}
}

func TestParseMessageBody_RealAttachmentStillReported(t *testing.T) {
	// Guard against overcorrection: a genuinely attached file must
	// still set hasAttachments on the legacy path.
	_, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(buildFiveAttachmentRaw(t, "gmail-mixed")))
	if !hasAttachments {
		t.Fatal("hasAttachments = false, want true for a message with real attachments")
	}
}
