package message

import (
	"bytes"
	"testing"
	"time"
)

// TestUpdateBody_PersistsEncryptedRawBodies is the audit finding-1
// regression test at the store layer: the on-demand body update must persist
// the raw S/MIME and PGP bodies (plus their encrypted flags) so an encrypted
// message stays decryptable after it is opened. Before the fix UpdateBody
// wrote only the text columns, so an encrypted message opened on-demand was
// marked body_fetched=1 with an empty body and its raw body discarded.
func TestUpdateBody_PersistsEncryptedRawBodies(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 42,
		MessageID: "<enc@example.com>", Subject: "Encrypted",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	smimeRaw := []byte("fake-smime-enveloped-data")
	pgpRaw := []byte("fake-pgp-encrypted-data")
	if err := store.UpdateBody(BodyUpdate{
		MessageID:      m.ID,
		SMIMERawBody:   smimeRaw,
		SMIMEEncrypted: true,
		PGPRawBody:     pgpRaw,
		PGPEncrypted:   true,
	}); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyFetched {
		t.Fatal("body_fetched not set by UpdateBody")
	}
	if !got.SMIMEEncrypted || !got.HasSMIME {
		t.Fatalf("S/MIME flags not persisted: encrypted=%v hasSMIME=%v", got.SMIMEEncrypted, got.HasSMIME)
	}
	if !got.PGPEncrypted || !got.HasPGP {
		t.Fatalf("PGP flags not persisted: encrypted=%v hasPGP=%v", got.PGPEncrypted, got.HasPGP)
	}

	// The decrypt-time getters must return the exact bytes that were stored:
	// decryption input preserved byte-for-byte is what "stays decryptable" means.
	if raw, err := store.GetSMIMERawBody(m.ID); err != nil {
		t.Fatalf("GetSMIMERawBody: %v", err)
	} else if !bytes.Equal(raw, smimeRaw) {
		t.Fatalf("S/MIME raw body not preserved: got %q, want %q", raw, smimeRaw)
	}
	if raw, err := store.GetPGPRawBody(m.ID); err != nil {
		t.Fatalf("GetPGPRawBody: %v", err)
	} else if !bytes.Equal(raw, pgpRaw) {
		t.Fatalf("PGP raw body not preserved: got %q, want %q", raw, pgpRaw)
	}
}

// TestUpdateBody_NullRawBodyWhenEmpty confirms a plain (unencrypted) body
// update stores NULL raw bodies, so HasSMIME/HasPGP stay false — an empty
// blob would falsely report the message as carrying S/MIME/PGP data.
func TestUpdateBody_NullRawBodyWhenEmpty(t *testing.T) {
	store, _, accountID, folderID := newUpsertTestStore(t)

	m := &Message{
		AccountID: accountID, FolderID: folderID, UID: 7,
		MessageID: "<plain@example.com>", Subject: "Plain",
		FromName: "Alice", FromEmail: "alice@example.com",
		Date: time.Now().UTC(),
	}
	if err := store.Upsert(m); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}

	if err := store.UpdateBody(BodyUpdate{
		MessageID: m.ID, BodyText: "hello", BodyHTML: "<p>hello</p>", Snippet: "hello",
	}); err != nil {
		t.Fatalf("UpdateBody: %v", err)
	}

	got, err := store.Get(m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.BodyFetched || got.BodyText != "hello" {
		t.Fatalf("plain body not stored: fetched=%v text=%q", got.BodyFetched, got.BodyText)
	}
	if got.HasSMIME || got.HasPGP {
		t.Fatalf("plain message falsely reports encrypted data: hasSMIME=%v hasPGP=%v", got.HasSMIME, got.HasPGP)
	}
	if raw, err := store.GetSMIMERawBody(m.ID); err != nil {
		t.Fatalf("GetSMIMERawBody: %v", err)
	} else if raw != nil {
		t.Fatalf("S/MIME raw body should be NULL for a plain message, got %q", raw)
	}
	if raw, err := store.GetPGPRawBody(m.ID); err != nil {
		t.Fatalf("GetPGPRawBody: %v", err)
	} else if raw != nil {
		t.Fatalf("PGP raw body should be NULL for a plain message, got %q", raw)
	}
}
