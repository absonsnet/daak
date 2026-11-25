package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/absonsnet/daak/internal/credentials"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/smime"
)

// TestPGPSignsWhenSMIMEUnavailable (fourth-audit finding 44): the
// S/MIME toggle on with no S/MIME certificate used to mean "signed by
// neither" — S/MIME did not apply (no cert) and PGP was skipped because
// the toggle was set. PGP is now gated on whether S/MIME actually
// applied, so the same send is PGP-signed.
func TestPGPSignsWhenSMIMEUnavailable(t *testing.T) {
	h := newSendQueueTestHarness(t)

	credStore, err := credentials.NewStore(h.db.DB, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := logging.WithComponent("test")
	h.svc.ops.credStore = credStore
	h.svc.ops.pgpSigner = pgp.NewSigner(h.svc.ops.pgpStore, credStore, log)
	h.svc.ops.smimeSigner = smime.NewSigner(h.svc.ops.smimeStore, credStore, log)

	// Real PGP key for me@example.com, made the account default.
	entity, err := openpgp.NewEntity("Me", "test", "me@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ident := range entity.Identities {
		if ident.SelfSignature != nil {
			ident.SelfSignature.KeyLifetimeSecs = nil
		}
	}
	pubArmored, err := pgp.ArmorPublicKey(entity)
	if err != nil {
		t.Fatal(err)
	}
	privArmored, err := pgp.ArmorPrivateKey(entity)
	if err != nil {
		t.Fatal(err)
	}
	key := pgp.ExtractKeyMetadata(entity)
	key.AccountID = "acc-1"
	if err := h.svc.ops.pgpStore.SaveKey(key, pubArmored); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.ops.pgpStore.SetDefaultKey("acc-1", key.ID); err != nil {
		t.Fatal(err)
	}
	if err := credStore.SetPGPPrivateKey(key.ID, []byte(privArmored)); err != nil {
		t.Fatal(err)
	}

	// S/MIME toggle ON (but no certificate exists for the account),
	// PGP sign requested: the old toggle-gated code sent this
	// unsigned by either; the fix PGP-signs it.
	msg := testComposeMessage()
	msg.SignMessage = true
	msg.PGPSignMessage = true
	if _, err, _ := h.svc.ops.sendMessageWithHook(context.Background(), "acc-1", msg, nil, nil); err != nil {
		t.Fatalf("sendMessageWithHook: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(h.sent))
	}
	raw := strings.ToLower(string(h.sent[0]))
	if !strings.Contains(raw, "pgp-signature") {
		t.Errorf("sent mail has no PGP signature; want it PGP-signed when S/MIME did not apply")
	}
	if strings.Contains(raw, "pkcs7-signature") {
		t.Errorf("sent mail has an S/MIME signature, want none (no certificate)")
	}

	// Control: same send without the PGP request stays unsigned.
	h.sent = nil
	msg.PGPSignMessage = false
	if _, err, _ := h.svc.ops.sendMessageWithHook(context.Background(), "acc-1", msg, nil, nil); err != nil {
		t.Fatalf("sendMessageWithHook (control): %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(h.sent))
	}
	raw = strings.ToLower(string(h.sent[0]))
	if strings.Contains(raw, "pgp-signature") || strings.Contains(raw, "pkcs7-signature") {
		t.Errorf("control send unexpectedly signed")
	}
}
