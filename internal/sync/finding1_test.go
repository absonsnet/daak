package sync

import (
	"bytes"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// Audit finding 1: an encrypted message fetched on-demand must keep its raw
// body — the raw bytes are the decryption input, and the old on-demand path
// stored only the (empty) text while marking the message body_fetched, so
// it could never be decrypted again.
//
// The on-demand path runs parseMessageBodyFull (same as the batch path) and
// stores the result via UpdateBody. These tests pin both halves without an
// IMAP server: the parser captures the raw body for encrypted fixtures, and
// the shared bodyUpdateFromProcessedBody mapping carries it into the store
// update (the store half is proven by TestUpdateBody_PersistsEncryptedRawBodies
// in internal/message).

const smimeEncryptedRaw = "From: alice@example.com\r\n" +
	"To: bob@example.com\r\n" +
	"Subject: Encrypted\r\n" +
	"Message-ID: <enc-smime@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: application/pkcs7-mime; smime-type=enveloped-data; name=\"smime.p7m\"\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"Content-Disposition: attachment; filename=\"smime.p7m\"\r\n" +
	"\r\n" +
	"MIIBmgIBAzCCAVcGCSqGSIb3DQEHAaCCAU0EggiFMAQIDAgEAMA0GCSqGSIb3\r\n"

const pgpEncryptedRaw = "From: alice@example.com\r\n" +
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

func TestParseEncryptedMessage_KeepsRawBody(t *testing.T) {
	e := &Engine{log: zerolog.Nop()}

	smime := e.parseMessageBodyFull([]byte(smimeEncryptedRaw), "m-smime", 30*time.Second)
	if smime == nil {
		t.Fatal("S/MIME parse returned nil")
	}
	if !smime.SMIMEEncrypted {
		t.Fatal("S/MIME encrypted message not flagged encrypted")
	}
	if !bytes.Equal(smime.SMIMERawBody, []byte(smimeEncryptedRaw)) {
		t.Fatalf("S/MIME raw body not preserved: got %d bytes, want %d", len(smime.SMIMERawBody), len(smimeEncryptedRaw))
	}
	if smime.BodyText != "" || smime.BodyHTML != "" {
		t.Fatalf("encrypted body text should stay empty until decrypted: text=%q html=%q", smime.BodyText, smime.BodyHTML)
	}

	pgp := e.parseMessageBodyFull([]byte(pgpEncryptedRaw), "m-pgp", 30*time.Second)
	if pgp == nil {
		t.Fatal("PGP parse returned nil")
	}
	if !pgp.PGPEncrypted {
		t.Fatal("PGP encrypted message not flagged encrypted")
	}
	if !bytes.Equal(pgp.PGPRawBody, []byte(pgpEncryptedRaw)) {
		t.Fatalf("PGP raw body not preserved: got %d bytes, want %d", len(pgp.PGPRawBody), len(pgpEncryptedRaw))
	}
	if pgp.BodyText != "" || pgp.BodyHTML != "" {
		t.Fatalf("encrypted body text should stay empty until decrypted: text=%q html=%q", pgp.BodyText, pgp.BodyHTML)
	}
}

// TestBodyUpdateFromProcessedBody_KeepsEncryptedRawBodies pins the mapping
// FetchMessageBody and the batch path share: the raw bodies captured by the
// parser must reach the store update, or the on-demand fetch silently
// drops them (the original finding-1 bug).
func TestBodyUpdateFromProcessedBody_KeepsEncryptedRawBodies(t *testing.T) {
	pb := &ProcessedBody{
		MessageID:      "m1",
		SMIMERawBody:   []byte("smime-raw"),
		SMIMEEncrypted: true,
		PGPRawBody:     []byte("pgp-raw"),
		PGPEncrypted:   true,
	}
	bu := bodyUpdateFromProcessedBody(pb)
	if bu.MessageID != "m1" {
		t.Fatalf("MessageID not carried: %q", bu.MessageID)
	}
	if !bytes.Equal(bu.SMIMERawBody, []byte("smime-raw")) || !bu.SMIMEEncrypted {
		t.Fatalf("S/MIME raw body/flag dropped: raw=%q encrypted=%v", bu.SMIMERawBody, bu.SMIMEEncrypted)
	}
	if !bytes.Equal(bu.PGPRawBody, []byte("pgp-raw")) || !bu.PGPEncrypted {
		t.Fatalf("PGP raw body/flag dropped: raw=%q encrypted=%v", bu.PGPRawBody, bu.PGPEncrypted)
	}

	plain := &ProcessedBody{MessageID: "m2", BodyText: "hello"}
	buPlain := bodyUpdateFromProcessedBody(plain)
	if buPlain.BodyText != "hello" || len(buPlain.SMIMERawBody) != 0 || len(buPlain.PGPRawBody) != 0 {
		t.Fatalf("plain mapping wrong: text=%q smime=%d pgp=%d", buPlain.BodyText, len(buPlain.SMIMERawBody), len(buPlain.PGPRawBody))
	}
}
