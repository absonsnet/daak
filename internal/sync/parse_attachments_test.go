package sync

import (
	"strings"
	"testing"
)

// Attachment-count tests for the sync parser (parseMessageBodyInternal).
// Regression investigation for the Nov 21 duplicate-attachment report:
// a message with 5 files showed each one twice (10 rows). These tests
// pin the parser's count for realistic MIME shapes, so a future doubling
// is caught here and not blamed on the display.

func buildFiveAttachmentRaw(t *testing.T, variant string) string {
	t.Helper()
	// Tiny 1x1 PNG payload, base64.
	img := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	pdf := "JVBERi0xLjQKJeLjz9MKMSAwIG9iago8PC9UeXBlIC9DYXRhbG9nPj4KZW5kb2JqCg=="

	var b strings.Builder
	b.WriteString("From: umar@example.com\r\nTo: abid@example.com\r\nSubject: CV and photos\r\nMessage-ID: <cv-1@example.com>\r\nMIME-Version: 1.0\r\n")

	switch variant {
	case "gmail-mixed":
		// Gmail web style: multipart/mixed > multipart/alternative +
		// plain attachments (one image carries a Content-ID).
		b.WriteString("Content-Type: multipart/mixed; boundary=\"MIXED\"\r\n\r\n")
		b.WriteString("--MIXED\r\nContent-Type: multipart/alternative; boundary=\"ALT\"\r\n\r\n")
		b.WriteString("--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPlease find my CV attached.\r\n")
		b.WriteString("--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body>Please find my CV attached.</body></html>\r\n")
		b.WriteString("--ALT--\r\n")
		b.WriteString("--MIXED\r\nContent-Type: image/jpeg; name=\"1000210286.jpg\"\r\nContent-Disposition: attachment; filename=\"1000210286.jpg\"\r\nContent-ID: <img1@example.com>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--MIXED\r\nContent-Type: image/jpeg; name=\"1000210287.jpg\"\r\nContent-Disposition: attachment; filename=\"1000210287.jpg\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--MIXED\r\nContent-Type: image/jpeg; name=\"1000212485.jpg\"\r\nContent-Disposition: attachment; filename=\"1000212485.jpg\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--MIXED\r\nContent-Type: application/pdf; name=\"Muhammad Umar CV-1.pdf\"\r\nContent-Disposition: attachment; filename=\"Muhammad Umar CV-1.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + pdf + "\r\n")
		b.WriteString("--MIXED\r\nContent-Type: application/pdf; name=\"douctoment.pdf\"\r\nContent-Disposition: attachment; filename=\"douctoment.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + pdf + "\r\n")
		b.WriteString("--MIXED--\r\n")
	case "inline-related":
		// Apple Mail / inline style: images inline with Content-ID
		// inside multipart/related, PDFs as plain attachments.
		b.WriteString("Content-Type: multipart/mixed; boundary=\"MIXED\"\r\n\r\n")
		b.WriteString("--MIXED\r\nContent-Type: multipart/related; boundary=\"REL\"\r\n\r\n")
		b.WriteString("--REL\r\nContent-Type: multipart/alternative; boundary=\"ALT\"\r\n\r\n")
		b.WriteString("--ALT\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee photos below.\r\n")
		b.WriteString("--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><img src=\"cid:img1@example.com\"></body></html>\r\n")
		b.WriteString("--ALT--\r\n")
		b.WriteString("--REL\r\nContent-Type: image/jpeg; name=\"1000210286.jpg\"\r\nContent-Disposition: inline; filename=\"1000210286.jpg\"\r\nContent-ID: <img1@example.com>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--REL\r\nContent-Type: image/jpeg; name=\"1000210287.jpg\"\r\nContent-Disposition: inline; filename=\"1000210287.jpg\"\r\nContent-ID: <img2@example.com>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--REL\r\nContent-Type: image/jpeg; name=\"1000212485.jpg\"\r\nContent-Disposition: inline; filename=\"1000212485.jpg\"\r\nContent-ID: <img3@example.com>\r\nContent-Transfer-Encoding: base64\r\n\r\n" + img + "\r\n")
		b.WriteString("--REL--\r\n")
		b.WriteString("--MIXED\r\nContent-Type: application/pdf; name=\"Muhammad Umar CV-1.pdf\"\r\nContent-Disposition: attachment; filename=\"Muhammad Umar CV-1.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + pdf + "\r\n")
		b.WriteString("--MIXED\r\nContent-Type: application/pdf; name=\"douctoment.pdf\"\r\nContent-Disposition: attachment; filename=\"douctoment.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + pdf + "\r\n")
		b.WriteString("--MIXED--\r\n")
	default:
		t.Fatalf("unknown variant %q", variant)
	}
	return b.String()
}

func TestParseAttachments_FiveFilesNotDoubled(t *testing.T) {
	for _, variant := range []string{"gmail-mixed", "inline-related"} {
		parsed := testEngine().parseMessageBodyInternal([]byte(buildFiveAttachmentRaw(t, variant)), "msg-1")
		if len(parsed.Attachments) != 5 {
			t.Fatalf("[%s] attachments = %d, want 5", variant, len(parsed.Attachments))
		}
		seen := map[string]int{}
		for _, a := range parsed.Attachments {
			seen[a.Filename]++
		}
		for name, n := range seen {
			if n != 1 {
				t.Fatalf("[%s] filename %q appears %d times, want 1", variant, name, n)
			}
		}
		if !parsed.HasAttachments {
			t.Fatalf("[%s] HasAttachments = false, want true", variant)
		}
	}
}

// Audit finding 22: these pin the LEGACY parseMessageBody path — the one
// buildMessageFromStreamedData (via FetchServerMessage) uses to set
// HasAttachments, which gates whether the extractor runs at all. The modern
// parser already classified these correctly; the legacy path did not.

// A single-part body that is itself an attachment must set hasAttachments
// (and must not dump the raw attachment bytes into the body text).
func TestParseMessageBody_SinglePartAttachment(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Report\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/zip; name=\"report.zip\"\r\n" +
		"Content-Disposition: attachment; filename=\"report.zip\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"UEsDBAoAAAAA\r\n"

	bodyText, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(raw))
	if !hasAttachments {
		t.Fatalf("hasAttachments = false, want true for single-part attachment")
	}
	if bodyText != "" {
		t.Errorf("bodyText = %q, want empty (attachment bytes are not body text)", bodyText)
	}
}

// A plain single-part text message is body text, not an attachment.
func TestParseMessageBody_SinglePartTextNotAttachment(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hello there\r\n"

	bodyText, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(raw))
	if hasAttachments {
		t.Fatalf("hasAttachments = true, want false for single-part text")
	}
	if strings.TrimSpace(bodyText) != "Hello there" {
		t.Errorf("bodyText = %q, want %q", bodyText, "Hello there")
	}
}

// A signed message's signature part must not set hasAttachments on its own:
// otherwise the message shows a paperclip with no downloadable rows (the
// extractor now correctly returns zero attachments for it).
func TestParseMessageBody_SignedMultipartNoAttachments(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Signed\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/signed; boundary=\"SIG\"; protocol=\"application/pkcs7-signature\"\r\n" +
		"\r\n" +
		"--SIG\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Signed body\r\n" +
		"--SIG\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n" +
		"Content-Disposition: attachment; filename=\"smime.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAECAwQFBgc=\r\n" +
		"--SIG--\r\n"

	_, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(raw))
	if hasAttachments {
		t.Fatalf("hasAttachments = true, want false (signature part only)")
	}
}

// A message whose whole body is a signature part is not an attachment.
func TestParseMessageBody_SinglePartSignatureNotAttachment(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Sig\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAECAwQFBgc=\r\n"

	_, _, hasAttachments, _ := testEngine().parseMessageBody([]byte(raw))
	if hasAttachments {
		t.Fatalf("hasAttachments = true, want false for single-part signature")
	}
}
