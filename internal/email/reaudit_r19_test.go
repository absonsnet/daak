package email

import (
	"testing"
)

// Re-audit finding R19 (extractor half): ExtractAttachments walked the raw
// bytes and classified the encrypted parts of a PGP/MIME or S/MIME
// message as downloadable attachments (usually attachment.bin), so a
// message opened from server search gained phantom attachment rows for
// its own encrypted payload. An encrypted message must yield zero
// attachments; a genuinely attached file must still be extracted.

const r19SMIMEEncryptedRaw = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Encrypted\r\n" +
	"Message-ID: <enc-smime@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"smime.p7m\"\r\n" +
	"\r\n" +
	"MIIBmgIBAzCCAVcGCSqGSIb3DQEHAaCCAU0EggiFMAQIDAgEAMA0GCSqGSIb3\r\n"

const r19PGPEncryptedRaw = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Encrypted\r\n" +
	"Message-ID: <enc-pgp@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/encrypted; boundary=\"PGP\"; protocol=\"application/pgp-encrypted\"\r\n" +
	"\r\n" +
	"--PGP\r\n" +
	"Content-Type: application/pgp-encrypted\r\n" +
	"Content-Description: PGP/MIME version identification\r\n" +
	"\r\n" +
	"Version: 1\r\n" +
	"\r\n" +
	"--PGP\r\n" +
	"Content-Type: application/octet-stream; name=\"encrypted.asc\"\r\n" +
	"Content-Description: OpenPGP encrypted message\r\n" +
	"Content-Disposition: inline; filename=\"encrypted.asc\"\r\n" +
	"\r\n" +
	"-----BEGIN PGP MESSAGE-----\r\n" +
	"wcBMA1234567890abcdef\r\n" +
	"-----END PGP MESSAGE-----\r\n" +
	"\r\n" +
	"--PGP--\r\n"

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

const r19RealAttachmentRaw = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: File\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/mixed; boundary=\"MIXED\"\r\n" +
	"\r\n" +
	"--MIXED\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"See attached.\r\n" +
	"--MIXED\r\n" +
	"Content-Type: application/pdf; name=\"doc.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"doc.pdf\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"JVBERi0xLjQK\r\n" +
	"--MIXED--\r\n"

func TestExtractAttachments_EncryptedYieldsNone(t *testing.T) {
	ex := NewAttachmentExtractor()
	for name, raw := range map[string]string{
		"smime":  r19SMIMEEncryptedRaw,
		"pgp":    r19PGPEncryptedRaw,
		"nested": r19NestedEncryptedRaw,
	} {
		atts, err := ex.ExtractAttachments("r19-"+name, []byte(raw))
		if err != nil {
			t.Fatalf("[%s] ExtractAttachments: %v", name, err)
		}
		if len(atts) != 0 {
			t.Fatalf("[%s] attachments = %d, want 0 for encrypted mail", name, len(atts))
		}
	}
}

func TestExtractAttachments_RealAttachmentStillExtracted(t *testing.T) {
	atts, err := NewAttachmentExtractor().ExtractAttachments("r19-real", []byte(r19RealAttachmentRaw))
	if err != nil {
		t.Fatalf("ExtractAttachments: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1 for a real attached file", len(atts))
	}
	if atts[0].Attachment.Filename != "doc.pdf" {
		t.Fatalf("filename = %q, want %q", atts[0].Attachment.Filename, "doc.pdf")
	}
}
