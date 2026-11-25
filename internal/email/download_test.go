package email

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"testing"

	"github.com/absonsnet/daak/internal/message"
)

// Audit finding 21: the sync parser stores part.Body, which go-message has
// already transfer-decoded. The downloader used to decode it AGAIN, so an
// attachment whose decoded content happens to be valid base64 (plausible for
// .txt/.csv/.ics) silently came back as shorter garbage bytes, disagreeing
// with the stored Size. These tests pin byte-identical downloads.

// base64LookingAttachment builds a multipart/mixed message with one text
// attachment whose DECODED content is itself valid base64.
func base64LookingAttachment(t *testing.T) ([]byte, []byte) {
	t.Helper()
	// Stored body (what sync records): valid base64 for "Hello, world!".
	stored := []byte("SGVsbG8sIHdvcmxkIQ==")
	encoded := base64.StdEncoding.EncodeToString(stored)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Data\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=\"BOUNDARY123\"\r\n\r\n")
	fmt.Fprintf(&buf, "--BOUNDARY123\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&buf, "See attached.\r\n")
	fmt.Fprintf(&buf, "--BOUNDARY123\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/csv; name=\"data.csv\"\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"data.csv\"\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n\r\n")
	fmt.Fprintf(&buf, "%s\r\n", encoded)
	fmt.Fprintf(&buf, "--BOUNDARY123--\r\n")
	return buf.Bytes(), stored
}

func TestExtractAttachmentContent_Base64LookingTextNotDoubleDecoded(t *testing.T) {
	raw, stored := base64LookingAttachment(t)

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(raw, "data.csv")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent error: %v", err)
	}
	if !bytes.Equal(got, stored) {
		t.Errorf("downloaded = %q (%d bytes), want stored body %q (%d bytes)",
			got, len(got), stored, len(stored))
	}

	// The extractor's Size must agree with the downloaded bytes too.
	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", raw)
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Attachment.Size != len(got) {
		t.Errorf("stored Size = %d, downloaded len = %d", atts[0].Attachment.Size, len(got))
	}
}

// Same double-decode hazard through the downloader's single-part branch:
// a whole-message text attachment must come back byte-identical.
func TestExtractAttachmentContent_SinglePartBase64LookingText(t *testing.T) {
	stored := []byte("SGVsbG8sIHdvcmxkIQ==")
	encoded := base64.StdEncoding.EncodeToString(stored)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Data\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/csv; name=\"data.csv\"\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"data.csv\"\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n\r\n")
	fmt.Fprintf(&buf, "%s\r\n", encoded)

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(buf.Bytes(), "data.csv")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent error: %v", err)
	}
	if !bytes.Equal(got, stored) {
		t.Errorf("downloaded = %q, want stored body %q", got, stored)
	}
}

// Quoted-printable variant: decoded content containing a "=3D" sequence must
// not be QP-decoded a second time.
func TestExtractAttachmentContent_QuotedPrintableNotDoubleDecoded(t *testing.T) {
	// Stored body contains a literal "=3D"; QP-encoding it doubles the "=".
	stored := []byte("price=3D100")
	encoded := "price=3D3D100"

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Data\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=\"BOUNDARY123\"\r\n\r\n")
	fmt.Fprintf(&buf, "--BOUNDARY123\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&buf, "See attached.\r\n")
	fmt.Fprintf(&buf, "--BOUNDARY123\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; name=\"note.txt\"\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=\"note.txt\"\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	fmt.Fprintf(&buf, "%s\r\n", encoded)
	fmt.Fprintf(&buf, "--BOUNDARY123--\r\n")

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(buf.Bytes(), "note.txt")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent error: %v", err)
	}
	if !bytes.Equal(got, stored) {
		t.Errorf("downloaded = %q, want stored body %q", got, stored)
	}
}

// Audit4 finding 29: two attachments with the same filename must be
// distinguished by size (and content-ID when present), not by name alone.
func TestExtractAttachmentContentFor_SameFilenameDisambiguatedBySize(t *testing.T) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: a@example.com\r\nTo: b@example.com\r\nSubject: Data\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B\"\r\n\r\n")
	fmt.Fprintf(&buf, "--B\r\nContent-Type: application/octet-stream; name=\"report.pdf\"\r\nContent-Disposition: attachment; filename=\"report.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", base64.StdEncoding.EncodeToString([]byte("short")))
	fmt.Fprintf(&buf, "--B\r\nContent-Type: application/octet-stream; name=\"report.pdf\"\r\nContent-Disposition: attachment; filename=\"report.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", base64.StdEncoding.EncodeToString([]byte("a much longer second file")))
	fmt.Fprintf(&buf, "--B--\r\n")
	raw := buf.Bytes()

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContentFor(raw, &message.Attachment{Filename: "report.pdf", Size: len("a much longer second file")})
	if err != nil {
		t.Fatalf("ExtractAttachmentContentFor error: %v", err)
	}
	if string(got) != "a much longer second file" {
		t.Errorf("downloaded = %q, want the second same-named file", got)
	}
	// Without a size to disambiguate, the first same-named file is returned.
	got, err = d.ExtractAttachmentContentFor(raw, &message.Attachment{Filename: "report.pdf"})
	if err != nil {
		t.Fatalf("ExtractAttachmentContentFor (no size) error: %v", err)
	}
	if string(got) != "short" {
		t.Errorf("downloaded = %q, want the first same-named file", got)
	}
}

// Audit4 finding 16: SaveAttachment must not slice an empty or short
// message ID out of range.
func TestSaveAttachment_ShortOrEmptyMessageID(t *testing.T) {
	d := NewAttachmentDownloader(t.TempDir())
	for _, id := range []string{"", "abc", "12345678"} {
		p, err := d.SaveAttachment(&message.Attachment{MessageID: id, Filename: "x.txt"}, []byte("hi"), "")
		if err != nil {
			t.Fatalf("SaveAttachment(%q) error: %v", id, err)
		}
		if p == "" {
			t.Fatalf("SaveAttachment(%q) returned empty path", id)
		}
	}
}

// Audit4 finding 29: Save All must not overwrite one file with another
// of the same name.
func TestDeduplicateFilename(t *testing.T) {
	dir := t.TempDir()
	used := map[string]bool{}
	if got := DeduplicateFilename(dir, "report.pdf", used); got != "report.pdf" {
		t.Errorf("first = %q, want report.pdf", got)
	}
	if got := DeduplicateFilename(dir, "report.pdf", used); got != "report (2).pdf" {
		t.Errorf("second = %q, want report (2).pdf", got)
	}
	if got := DeduplicateFilename(dir, "report.pdf", used); got != "report (3).pdf" {
		t.Errorf("third = %q, want report (3).pdf", got)
	}
	// A file already on disk also forces a new name.
	if err := writeTestFile(dir, "notes.txt", "x"); err != nil {
		t.Fatal(err)
	}
	if got := DeduplicateFilename(dir, "notes.txt", map[string]bool{}); got != "notes (2).txt" {
		t.Errorf("on-disk collision = %q, want notes (2).txt", got)
	}
}

func writeTestFile(dir, name, content string) error {
	return os.WriteFile(dir+"/"+name, []byte(content), 0600)
}
