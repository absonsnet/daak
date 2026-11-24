package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestFallbackFilename(t *testing.T) {
	cases := []struct {
		contentType string
		want        string
	}{
		{"text/calendar", "invite.ics"},
		{"image/png", "attachment.png"},
		{"image/jpeg", "attachment.jpeg"},
		{"application/pdf", "attachment.pdf"},
		{"application/x-unknown-thing", "attachment.bin"},
		{"", "attachment.bin"},
	}
	for _, c := range cases {
		if got := FallbackFilename(c.contentType); got != c.want {
			t.Errorf("FallbackFilename(%q) = %q, want %q", c.contentType, got, c.want)
		}
	}
}

// inviteMessage builds a multipart/mixed message with a text/html body and a
// nameless text/calendar part carrying an Outlook-style Content-ID — the
// #370 meeting-invite shape.
func inviteMessage(ics []byte) []byte {
	var buf bytes.Buffer
	boundary := "BOUNDARY123"
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Meeting\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&buf, "<p>You're invited</p>\r\n")
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-ID: <ABC123DEF456@SV1.prod.outlook.com>\r\n\r\n")
	buf.Write(ics)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)
	return buf.Bytes()
}

func TestExtractAttachments_CalendarInviteNamedICS(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR")
	raw := inviteMessage(ics)

	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", raw)
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	att := atts[0]
	if att.Attachment.Filename != "invite.ics" {
		t.Errorf("filename = %q, want invite.ics", att.Attachment.Filename)
	}
	if !att.Attachment.IsInline {
		t.Errorf("expected invite part (Content-ID, no disposition) to be inline")
	}
	if !strings.Contains(att.Attachment.ContentID, "prod.outlook.com") {
		t.Errorf("content id not preserved: %q", att.Attachment.ContentID)
	}
}

// Audit finding 22: a single-part message whose whole body is an attachment
// (e.g. a DMARC report as application/zip) must list one downloadable row.
// The decoded content is itself valid base64 ("SGVsbG8sIHdvcmxkIQ==" decodes
// to "Hello, world!"), which also pins finding 21: the extractor must return
// the stored body byte-identical, not base64-decode it a second time.
func TestExtractAttachments_SinglePartAttachment(t *testing.T) {
	// Decoded (stored) body: base64-looking text.
	stored := []byte("SGVsbG8sIHdvcmxkIQ==")
	encoded := base64.StdEncoding.EncodeToString(stored)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Report\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: application/pdf; name=\"report.pdf\"\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"report.pdf\"\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n\r\n")
	fmt.Fprintf(&buf, "%s\r\n", encoded)

	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", buf.Bytes())
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	att := atts[0]
	if att.Attachment.Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", att.Attachment.Filename)
	}
	if !bytes.Equal(att.Content, stored) {
		t.Errorf("content = %q, want stored body %q (double-decoded?)", att.Content, stored)
	}
	if att.Attachment.Size != len(stored) {
		t.Errorf("size = %d, want %d", att.Attachment.Size, len(stored))
	}
}

// Audit finding 22: a plain single-part text message is body text, not an
// attachment — the single-part branch must not over-classify.
func TestExtractAttachments_SinglePartTextIsNotAttachment(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Hi\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Hello there\r\n"

	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", []byte(raw))
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 0 {
		t.Fatalf("expected 0 attachments, got %d", len(atts))
	}
}

// Audit finding 22: S/MIME and PGP signature parts must not be exposed as
// downloadable attachments — in a multipart/signed message, as a bare
// implicit part, or as a single-part body.
func TestExtractAttachments_SignaturePartsExcluded(t *testing.T) {
	signed := func(sigType, sigName string) []byte {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "From: a@example.com\r\n")
		fmt.Fprintf(&buf, "To: b@example.com\r\n")
		fmt.Fprintf(&buf, "Subject: Signed\r\n")
		fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
		fmt.Fprintf(&buf, "Content-Type: multipart/signed; boundary=\"SIG\"; protocol=\"application/pkcs7-signature\"\r\n\r\n")
		fmt.Fprintf(&buf, "--SIG\r\n")
		fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
		fmt.Fprintf(&buf, "Signed body\r\n")
		fmt.Fprintf(&buf, "--SIG\r\n")
		fmt.Fprintf(&buf, "Content-Type: %s; name=\"%s\"\r\n", sigType, sigName)
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"%s\"\r\n", sigName)
		fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n\r\n")
		fmt.Fprintf(&buf, "AAECAwQFBgc=\r\n")
		fmt.Fprintf(&buf, "--SIG--\r\n")
		return buf.Bytes()
	}

	e := NewAttachmentExtractor()
	for _, tc := range []struct{ sigType, sigName string }{
		{"application/pkcs7-signature", "smime.p7s"},
		{"application/x-pkcs7-signature", "smime.p7s"},
		{"application/pgp-signature", "signature.asc"},
	} {
		atts, err := e.ExtractAttachments("msg-1", signed(tc.sigType, tc.sigName))
		if err != nil {
			t.Fatalf("[%s] ExtractAttachments error: %v", tc.sigType, err)
		}
		if len(atts) != 0 {
			t.Fatalf("[%s] expected 0 attachments, got %d (%q)", tc.sigType, len(atts), atts[0].Attachment.Filename)
		}
	}

	// A message whose whole body IS a signature part lists nothing either.
	raw := "From: a@example.com\r\n" +
		"To: b@example.com\r\n" +
		"Subject: Sig\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: application/pkcs7-signature; name=\"smime.p7s\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"AAECAwQFBgc=\r\n"
	atts, err := e.ExtractAttachments("msg-1", []byte(raw))
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 0 {
		t.Fatalf("single-part signature: expected 0 attachments, got %d", len(atts))
	}
}
func TestExtractAttachmentContent_FallbackNameLockstep(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR")
	raw := inviteMessage(ics)

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(raw, "invite.ics")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent(invite.ics) error: %v", err)
	}
	if !bytes.Equal(got, ics) {
		t.Errorf("content mismatch: got %q, want %q", got, ics)
	}
}
