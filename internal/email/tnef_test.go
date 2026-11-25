package email

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

// tnefObj encodes a single TNEF object in the layout teamwork/tnef decodes:
// level(1) name(2,LE) type(2,LE) length(4,LE) data checksum(2, ignored).
func tnefObj(level, name int, data []byte) []byte {
	var b bytes.Buffer
	b.WriteByte(byte(level))
	_ = binary.Write(&b, binary.LittleEndian, uint16(name))
	_ = binary.Write(&b, binary.LittleEndian, uint16(0)) // type — ignored by decoder
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	_ = binary.Write(&b, binary.LittleEndian, uint16(0)) // checksum — ignored
	return b.Bytes()
}

// buildWinmailDat produces a minimal valid TNEF container wrapping one
// attachment: signature + key, then ATTATTACHRENDDATA (starts an attachment),
// ATTATTACHTITLE (filename), ATTATTACHDATA (content).
func buildWinmailDat(filename string, content []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(0x223e9f78)) // tnefSignature
	_ = binary.Write(&b, binary.LittleEndian, uint16(0))          // key
	b.Write(tnefObj(0x02, 0x9002, []byte{0}))                     // ATTATTACHRENDDATA
	b.Write(tnefObj(0x02, 0x8010, append([]byte(filename), 0)))   // ATTATTACHTITLE
	b.Write(tnefObj(0x02, 0x800f, content))                       // ATTATTACHDATA
	return b.Bytes()
}

// b64wrap base64-encodes bytes wrapped at 76 cols with CRLF, as MIME expects.
func b64wrap(b []byte) string {
	enc := base64.StdEncoding.EncodeToString(b)
	var sb strings.Builder
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		sb.WriteString(enc[i:end])
		sb.WriteString("\r\n")
	}
	return sb.String()
}

// winmailMessage builds a multipart/mixed message whose only attachment part is
// a base64-encoded winmail.dat wrapping the given inner file.
func winmailMessage(innerName string, innerContent []byte) []byte {
	tnefBytes := buildWinmailDat(innerName, innerContent)
	var sb strings.Builder
	sb.WriteString("From: a@example.com\r\n")
	sb.WriteString("To: b@example.com\r\n")
	sb.WriteString("Subject: tnef test\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n")
	sb.WriteString("--BOUND\r\n")
	sb.WriteString("Content-Type: text/plain\r\n\r\n")
	sb.WriteString("hello\r\n")
	sb.WriteString("--BOUND\r\n")
	sb.WriteString("Content-Type: application/ms-tnef; name=\"winmail.dat\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"winmail.dat\"\r\n\r\n")
	sb.WriteString(b64wrap(tnefBytes))
	sb.WriteString("\r\n--BOUND--\r\n")
	return []byte(sb.String())
}

func TestDecodeTNEFAttachments(t *testing.T) {
	content := []byte("%PDF-1.4 not really a pdf")
	atts := DecodeTNEFAttachments(buildWinmailDat("report.pdf", content))
	if len(atts) != 1 {
		t.Fatalf("expected 1 inner attachment, got %d", len(atts))
	}
	if atts[0].Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", atts[0].Filename)
	}
	if !bytes.Equal(atts[0].Content, content) {
		t.Errorf("content mismatch: got %q", atts[0].Content)
	}
	if atts[0].ContentType != "application/pdf" {
		t.Errorf("contentType = %q, want application/pdf", atts[0].ContentType)
	}
}

func TestDecodeTNEFAttachments_Invalid(t *testing.T) {
	if got := DecodeTNEFAttachments([]byte("not a tnef container")); got != nil {
		t.Errorf("expected nil for non-TNEF bytes, got %v", got)
	}
}

// TestExtractAttachments_CorruptWinmailDat (fourth-audit finding 50): a
// corrupt winmail.dat must still surface as one downloadable row named
// winmail.dat, like the sync parser does. Before the fix this extractor
// — the path a message opened from server search takes — returned
// nothing on TNEF decode failure while the message still carried its
// has_attachments flag, so the paperclip opened an empty list.
func TestExtractAttachments_CorruptWinmailDat(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("From: a@example.com\r\n")
	sb.WriteString("To: b@example.com\r\n")
	sb.WriteString("Subject: corrupt tnef\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/mixed; boundary=\"BOUND\"\r\n\r\n")
	sb.WriteString("--BOUND\r\n")
	sb.WriteString("Content-Type: text/plain\r\n\r\n")
	sb.WriteString("hello\r\n")
	sb.WriteString("--BOUND\r\n")
	sb.WriteString("Content-Type: application/ms-tnef; name=\"winmail.dat\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: base64\r\n")
	sb.WriteString("Content-Disposition: attachment; filename=\"winmail.dat\"\r\n\r\n")
	sb.WriteString(b64wrap([]byte("not a tnef container at all")))
	sb.WriteString("\r\n--BOUND--\r\n")

	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", []byte(sb.String()))
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment (the winmail.dat container), got %d", len(atts))
	}
	att := atts[0]
	if att.Attachment.Filename != "winmail.dat" {
		t.Errorf("filename = %q, want winmail.dat", att.Attachment.Filename)
	}
	if att.Attachment.ContentType != "application/ms-tnef" {
		t.Errorf("content type = %q, want application/ms-tnef", att.Attachment.ContentType)
	}
	if !bytes.Equal(att.Content, []byte("not a tnef container at all")) {
		t.Errorf("content = %q, want the raw container bytes", att.Content)
	}
}

// TestExtractAttachmentContent_TNEF proves the download path resolves an inner
// TNEF file by the same name the sync extractor would have stored.
func TestExtractAttachmentContent_TNEF(t *testing.T) {
	content := []byte("inner pdf bytes 12345")
	raw := winmailMessage("report.pdf", content)

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(raw, "report.pdf")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent(report.pdf) error: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}
