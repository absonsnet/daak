package mailmerge

// Phase 3 signing tests. These prove signing with REAL keys and the
// codebase's own verification paths — not flag assertions:
//
//   - S/MIME: a self-signed cert + RSA key in a migrated test DB, the
//     real smime.Signer signs each row, and the real smime.Verifier
//     verifies the signature (StatusSelfSigned: cryptographically
//     valid, self-signed cert) and unwraps the row's own personalised
//     body.
//   - PGP: a generated openpgp entity in the same DB, the real
//     pgp.Signer signs each row, and the real pgp.Verifier verifies it
//     (StatusSigned) once the public key is cached as a sender key.
//
// The fake MessageSigner below only adapts the real signers to the
// service's MessageSigner interface (the host adapter in
// app/extension_mail_merge.go does the same against composeOps); the
// signing and verification themselves are production code.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/smime"
	"github.com/rs/zerolog"
)

const testSigningAccount = "acc1"
const testSigningEmail = "sender@example.com"

// --- fixtures ------------------------------------------------------------

func newTestSigningDB(t *testing.T) (*database.DB, *credentials.Store) {
	t.Helper()
	tmp := t.TempDir()
	db, err := database.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		INSERT INTO accounts (id, name, email, imap_host, smtp_host, username)
		VALUES (?, 'Test', ?, 'imap.example.com', 'smtp.example.com', ?)
	`, testSigningAccount, testSigningEmail, testSigningEmail); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	creds, err := credentials.NewStore(db.DB, tmp)
	if err != nil {
		t.Fatalf("NewStore credentials: %v", err)
	}
	return db, creds
}

// newTestSMIMESigner creates a self-signed cert + RSA key for the test
// account, stores both through the real stores, and returns the real
// S/MIME signer and verifier.
func newTestSMIMESigner(t *testing.T, db *database.DB, creds *credentials.Store) (*smime.Signer, *smime.Verifier) {
	t.Helper()
	log := zerolog.Nop()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	serial := big.NewInt(time.Now().UnixNano())
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Sender"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection},
		EmailAddresses: []string{
			testSigningEmail,
		},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	fp := sha256.Sum256(der)
	store := smime.NewStore(db.DB, log)
	cert := &smime.Certificate{
		AccountID:    testSigningAccount,
		Email:        testSigningEmail,
		Subject:      "CN=Sender",
		Issuer:       "CN=Sender",
		SerialNumber: fmt.Sprint(serial),
		Fingerprint:  hex.EncodeToString(fp[:]),
		NotBefore:    tmpl.NotBefore,
		NotAfter:     tmpl.NotAfter,
		IsDefault:    true,
	}
	if err := store.SaveCertificate(cert, string(certPEM)); err != nil {
		t.Fatalf("save certificate: %v", err)
	}
	if err := store.SetDefaultCertificate(testSigningAccount, cert.ID); err != nil {
		t.Fatalf("set default certificate: %v", err)
	}
	if err := creds.SetSMIMEPrivateKey(cert.ID, privPEM); err != nil {
		t.Fatalf("store private key: %v", err)
	}
	return smime.NewSigner(store, creds, log), smime.NewVerifier(store, log)
}

// newTestPGPSigner generates an openpgp entity for the test account,
// stores it through the real stores, and returns the real PGP signer
// and verifier. The public key is cached as a sender key so the
// verifier's keyring (built from sender keys only) can verify the
// signatures.
func newTestPGPSigner(t *testing.T, db *database.DB, creds *credentials.Store) (*pgp.Signer, *pgp.Verifier) {
	t.Helper()
	log := zerolog.Nop()
	entity, err := openpgp.NewEntity("Sender", "", testSigningEmail, nil)
	if err != nil {
		t.Fatalf("generate PGP entity: %v", err)
	}
	// openpgp.NewEntity(nil config) sets KeyLifetimeSecs=0 (instant
	// expiry); clear it like pgp's own generateTestKey does.
	for _, ident := range entity.Identities {
		if ident.SelfSignature != nil {
			ident.SelfSignature.KeyLifetimeSecs = nil
		}
	}
	pubArmor, err := pgp.ArmorPublicKey(entity)
	if err != nil {
		t.Fatalf("armor public key: %v", err)
	}
	privArmor, err := pgp.ArmorPrivateKey(entity)
	if err != nil {
		t.Fatalf("armor private key: %v", err)
	}
	meta := pgp.ExtractKeyMetadata(entity)
	meta.AccountID = testSigningAccount
	meta.IsDefault = true
	store := pgp.NewStore(db.DB, log)
	if err := store.SaveKey(meta, pubArmor); err != nil {
		t.Fatalf("save PGP key: %v", err)
	}
	if err := store.SetDefaultKey(testSigningAccount, meta.ID); err != nil {
		t.Fatalf("set default PGP key: %v", err)
	}
	if err := creds.SetPGPPrivateKey(meta.ID, []byte(privArmor)); err != nil {
		t.Fatalf("store PGP private key: %v", err)
	}
	if err := store.CacheSenderKey(testSigningEmail, pubArmor, "test"); err != nil {
		t.Fatalf("cache sender key: %v", err)
	}
	return pgp.NewSigner(store, creds, log), pgp.NewVerifier(store, log)
}

// testMessageSigner adapts one real signer to the service's
// MessageSigner interface (the host adapter in
// app/extension_mail_merge.go does the same against composeOps).
// failFor makes SignMessage fail when the raw bytes contain it — the
// mid-run signing-failure test.
type testMessageSigner struct {
	kind    string
	sign    func(accountID, fromEmail string, raw []byte) ([]byte, error)
	failFor string
}

func (f *testMessageSigner) SignMessage(accountID, fromEmail string, raw []byte) ([]byte, string, error) {
	if f.failFor != "" && bytes.Contains(raw, []byte(f.failFor)) {
		return nil, "", errors.New("test signing failure")
	}
	signed, err := f.sign(accountID, fromEmail, raw)
	if err != nil {
		return nil, "", err
	}
	return signed, f.kind, nil
}

func (f *testMessageSigner) SigningKind(_, _ string) string { return f.kind }
func (f *testMessageSigner) DefaultSign(_, _ string) bool   { return true }

func testSigningTemplate(kind string) Template {
	tmpl := testTemplate()
	tmpl.Sign = true
	tmpl.SignKind = kind
	return tmpl
}

func runSigningSend(t *testing.T, signer MessageSigner, kind string) (*fakeSender, Outcome) {
	t.Helper()
	sender := &fakeSender{}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
		MessageSigner: signer,
	})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template:    testSigningTemplate(kind),
		Rows:        testRows(),
		DeliverMode: "send",
		AccountID:   testSigningAccount,
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return sender, out
}

// --- S/MIME ---------------------------------------------------------------

func TestServiceSendSignsEachRowSMIME(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, verifier := newTestSMIMESigner(t, db, creds)
	adapter := &testMessageSigner{
		kind: "smime",
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	sender, out := runSigningSend(t, adapter, "smime")
	if out.SentCount != 3 || out.FailedCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent %d messages", len(sender.sent))
	}
	// Each row's bytes must be a valid S/MIME signature over THAT
	// row's personalised body — signing the template once would fail
	// this: the unwrapped bodies must each contain their own code.
	seenBodies := map[string]bool{}
	for i, m := range sender.sent {
		raw := string(m.raw)
		if !strings.Contains(raw, "multipart/signed") || !strings.Contains(raw, "application/pkcs7-signature") {
			t.Fatalf("row %d: not S/MIME signed:\n%s", i, raw[:min(500, len(raw))])
		}
		result, unwrapped := verifier.VerifyAndUnwrap(m.raw)
		if result == nil {
			t.Fatalf("row %d: verifier found no signature", i)
		}
		if result.Status != smime.StatusSelfSigned {
			t.Fatalf("row %d: verify status %q (%s), want %q", i, result.Status, result.ErrorMessage, smime.StatusSelfSigned)
		}
		code := testRows()[i]["Code"]
		if !bytes.Contains(unwrapped, []byte(code)) {
			t.Fatalf("row %d: unwrapped body does not contain its code %q", i, code)
		}
		seenBodies[string(unwrapped)] = true
	}
	if len(seenBodies) != 3 {
		t.Fatalf("unwrapped bodies are not distinct per row — template signed once?")
	}
}

// --- PGP ------------------------------------------------------------------

func TestServiceSendSignsEachRowPGP(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, verifier := newTestPGPSigner(t, db, creds)
	adapter := &testMessageSigner{
		kind: "pgp",
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	sender, out := runSigningSend(t, adapter, "pgp")
	if out.SentCount != 3 || out.FailedCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent %d messages", len(sender.sent))
	}
	seenBodies := map[string]bool{}
	for i, m := range sender.sent {
		raw := string(m.raw)
		if !strings.Contains(raw, "multipart/signed") || !strings.Contains(raw, "application/pgp-signature") {
			t.Fatalf("row %d: not PGP signed", i)
		}
		result, unwrapped := verifier.VerifyAndUnwrap(m.raw)
		if result == nil {
			t.Fatalf("row %d: verifier found no signature", i)
		}
		if result.Status != pgp.StatusSigned {
			t.Fatalf("row %d: verify status %q (%s), want %q", i, result.Status, result.ErrorMessage, pgp.StatusSigned)
		}
		if result.SignerEmail != testSigningEmail {
			t.Fatalf("row %d: signer %q, want %q", i, result.SignerEmail, testSigningEmail)
		}
		code := testRows()[i]["Code"]
		if !bytes.Contains(unwrapped, []byte(code)) {
			t.Fatalf("row %d: unwrapped body does not contain its code %q", i, code)
		}
		seenBodies[string(unwrapped)] = true
	}
	if len(seenBodies) != 3 {
		t.Fatalf("unwrapped bodies are not distinct per row — template signed once?")
	}
}

// --- signing off / unavailable --------------------------------------------

func TestServiceSignOffLeavesUnsignedOutput(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, _ := newTestSMIMESigner(t, db, creds)
	adapter := &testMessageSigner{
		kind: "smime",
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	sender := &fakeSender{}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
		MessageSigner: adapter,
	})
	tmpl := testTemplate() // Sign=false
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: tmpl, Rows: testRows(), DeliverMode: "send", AccountID: testSigningAccount,
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.SentCount != 3 {
		t.Fatalf("outcome %+v", out)
	}
	for i, m := range sender.sent {
		if strings.Contains(string(m.raw), "multipart/signed") {
			t.Fatalf("row %d: signed although signing was off", i)
		}
		if !bytes.Contains(m.raw, []byte(testRows()[i]["Code"])) {
			t.Fatalf("row %d: body missing its code", i)
		}
	}
}

// Signing requested with no signer configured (no key/certificate in
// the host) must fail the run BEFORE anything is sent, saved or
// queued — never mid-run, never unsigned.
func TestServiceSigningWithoutKeyFailsBeforeSending(t *testing.T) {
	// Send mode: nothing transmitted.
	sender := &fakeSender{}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
		// No MessageSigner.
	})
	_, err := svc.Execute(context.Background(), RunRequest{
		Template:    testSigningTemplate("smime"),
		Rows:        testRows(),
		DeliverMode: "send",
		AccountID:   testSigningAccount,
	}, nil)
	if err == nil {
		t.Fatal("expected Execute to fail with no signer configured")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent %d messages before the signing failure", len(sender.sent))
	}

	// Drafts mode: nothing saved.
	saver := &fakeDraftSaver{}
	svc = NewService(ServiceDeps{DraftSaver: saver})
	_, err = svc.Execute(context.Background(), RunRequest{
		Template:    testSigningTemplate("smime"),
		Rows:        testRows(),
		DeliverMode: "drafts",
		AccountID:   testSigningAccount,
	}, nil)
	if err == nil {
		t.Fatal("expected Execute (drafts) to fail with no signer configured")
	}
	if len(saver.saved) != 0 {
		t.Fatalf("saved %d drafts before the signing failure", len(saver.saved))
	}

	// Queue mode: nothing queued.
	queued := 0
	svc = NewService(ServiceDeps{})
	_, err = svc.RenderForQueue(context.Background(), RunRequest{
		Template:    testSigningTemplate("smime"),
		Rows:        testRows(),
		DeliverMode: "later",
		AccountID:   testSigningAccount,
	}, func(QueuedMessage) error { queued++; return nil }, nil)
	if err == nil {
		t.Fatal("expected RenderForQueue to fail with no signer configured")
	}
	if queued != 0 {
		t.Fatalf("queued %d messages before the signing failure", queued)
	}
}

// A signing run whose key disappears (signer errors) fails validation
// at CheckSigningForRun time, not mid-run.
func TestServiceCheckSigningForRun(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, _ := newTestSMIMESigner(t, db, creds)
	adapter := &testMessageSigner{
		kind: "smime",
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	svc := NewService(ServiceDeps{MessageSigner: adapter})
	kind, err := svc.CheckSigningForRun(RunRequest{
		Template: testSigningTemplate("smime"), Rows: testRows(), AccountID: testSigningAccount,
	})
	if err != nil {
		t.Fatalf("CheckSigningForRun: %v", err)
	}
	if kind != "smime" {
		t.Fatalf("kind %q, want %q", kind, "smime")
	}
	info := svc.SigningInfo(testSigningAccount, testSigningEmail)
	if !info.Available || info.Kind != "smime" || !info.DefaultSign {
		t.Fatalf("SigningInfo %+v", info)
	}

	// No signer at all: unavailable, with a reason.
	nosign := NewService(ServiceDeps{})
	if _, err := nosign.CheckSigningForRun(RunRequest{
		Template: testSigningTemplate("smime"), Rows: testRows(), AccountID: testSigningAccount,
	}); err == nil {
		t.Fatal("expected CheckSigningForRun to fail with no signer")
	}
	info = nosign.SigningInfo(testSigningAccount, testSigningEmail)
	if info.Available || info.Reason == "" {
		t.Fatalf("SigningInfo %+v, want unavailable with a reason", info)
	}
}

// --- drafts + queue -------------------------------------------------------

// Drafts mode preserves the signing intent on the draft so sending the
// draft later signs it (draft.SignMessage / draft.PGPSignMessage are
// what SaveDraft stores and the send path reads back).
func TestServiceDraftsPreserveSigningIntent(t *testing.T) {
	for _, kind := range []string{"smime", "pgp"} {
		saver := &fakeDraftSaver{}
		svc := NewService(ServiceDeps{
			DraftSaver: saver,
			MessageSigner: &testMessageSigner{
				kind: kind,
				sign: func(_, _ string, raw []byte) ([]byte, error) { return raw, nil },
			},
		})
		out, err := svc.Execute(context.Background(), RunRequest{
			Template:    testSigningTemplate(kind),
			Rows:        testRows(),
			DeliverMode: "drafts",
			AccountID:   testSigningAccount,
		}, nil)
		if err != nil {
			t.Fatalf("kind %s: Execute: %v", kind, err)
		}
		if out.SentCount != 3 || len(saver.saved) != 3 {
			t.Fatalf("kind %s: outcome %+v, saved %d", kind, out, len(saver.saved))
		}
		for i, m := range saver.saved {
			if kind == "smime" && (!m.SignMessage || m.PGPSignMessage) {
				t.Fatalf("kind %s row %d: SignMessage=%v PGPSignMessage=%v", kind, i, m.SignMessage, m.PGPSignMessage)
			}
			if kind == "pgp" && (m.SignMessage || !m.PGPSignMessage) {
				t.Fatalf("kind %s row %d: SignMessage=%v PGPSignMessage=%v", kind, i, m.SignMessage, m.PGPSignMessage)
			}
		}
	}
}

// Queue mode signs at RENDER time: the stored bytes already carry a
// valid signature, so the dispatcher never needs to sign (or re-render).
func TestServiceQueueSignsAtRenderTime(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, verifier := newTestSMIMESigner(t, db, creds)
	adapter := &testMessageSigner{
		kind: "smime",
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	svc := NewService(ServiceDeps{MessageSigner: adapter})
	var queued []QueuedMessage
	out, err := svc.RenderForQueue(context.Background(), RunRequest{
		Template:    testSigningTemplate("smime"),
		Rows:        testRows(),
		DeliverMode: "later",
		AccountID:   testSigningAccount,
	}, func(qm QueuedMessage) error { queued = append(queued, qm); return nil }, nil)
	if err != nil {
		t.Fatalf("RenderForQueue: %v", err)
	}
	if out.QueuedCount != 3 || len(queued) != 3 {
		t.Fatalf("outcome %+v, queued %d", out, len(queued))
	}
	for i, qm := range queued {
		result, unwrapped := verifier.VerifyAndUnwrap(qm.Raw)
		if result == nil || result.Status != smime.StatusSelfSigned {
			t.Fatalf("row %d: queued bytes do not verify: %+v", i, result)
		}
		if !bytes.Contains(unwrapped, []byte(testRows()[i]["Code"])) {
			t.Fatalf("row %d: queued bytes do not contain their code", i)
		}
	}
}

// --- mid-run failure ------------------------------------------------------

// A row whose signing fails mid-run is recorded as failed and the run
// continues with the remaining rows — and the failed row is never sent
// unsigned.
func TestServiceSignFailureSkipsRowAndContinues(t *testing.T) {
	db, creds := newTestSigningDB(t)
	signer, _ := newTestSMIMESigner(t, db, creds)
	adapter := &testMessageSigner{
		kind:    "smime",
		failFor: "jane@example.com", // the middle row
		sign: func(accountID, fromEmail string, raw []byte) ([]byte, error) {
			return signer.SignMessage(accountID, fromEmail, raw)
		},
	}
	sender := &fakeSender{}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
		MessageSigner: adapter,
	})
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template:    testSigningTemplate("smime"),
		Rows:        testRows(),
		DeliverMode: "send",
		AccountID:   testSigningAccount,
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.SentCount != 2 || out.FailedCount != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d messages", len(sender.sent))
	}
	for _, m := range sender.sent {
		if bytes.Contains(m.raw, []byte("jane@example.com")) {
			t.Fatal("failed row was sent")
		}
		if !strings.Contains(string(m.raw), "multipart/signed") {
			t.Fatal("a sent row is unsigned")
		}
	}
	failed := false
	for _, r := range results {
		if r.Status == RowFailed && strings.HasPrefix(r.Error, "sign:") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("no row failed with a signing error: %+v", results)
	}
}
