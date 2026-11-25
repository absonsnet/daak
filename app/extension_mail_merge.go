package app

import (
	"context"
	"fmt"

	extmailmergebe "github.com/absonsnet/daak/extensions/mail-merge/backend"
	"github.com/absonsnet/daak/internal/contact"
	hostmm "github.com/absonsnet/daak/internal/mailmerge"
	"github.com/absonsnet/daak/internal/smtp"
)

// initMailMergeExtension wires the Mail Merge extension's Bridge into App
// during Startup. All bridge logic lives in
// extensions/mail-merge/backend/; the merge engine, sources and run
// orchestration live in internal/mailmerge; this file exists ONLY so the
// host can supply the bridge with its host-provided dependencies (the
// host-constructed Service object, settings store, paths, coreapi handle)
// and so the embedded-field promotion makes the bridge methods
// Wails-bindable.
//
// Lightweight-by-default invariant: the Bridge struct allocation is the
// entire footprint until the first enabled `MailMerge_*` Wails call. At
// that point, `MailMergeBridge.ensureInit()` opens the per-extension
// SQLite and applies pending migrations. Disabled extensions contribute
// zero work.
//
// Per docs/EXT_RULES.md R2, this file holds NO closures wrapping
// `internal/*` calls. The host's touchpoints are plain adapter structs
// below (each wraps a host store/ops value and implements one narrow
// hostmm interface by calling its methods) plus the standard
// `coreapi.Core` surface for the toolbar button + settings tab.
func (a *App) initMailMergeExtension() {
	mailMergeCore := newCoreForExtension(a, a.mailMergeExt)

	a.MailMergeBridge = extmailmergebe.NewMailMergeBridge(extmailmergebe.MailMergeBridgeDeps{
		SettingsStore: a.settingsStore,
		Paths:         a.paths,
		Core:          mailMergeCore,
		Service:       newMailMergeService(&a.composeOps, &appMailMergeDraftSaver{a: a}, a.contactStore, &mailMergeSentSaveFailureRecorder{get: func() *sendQueueService { return a.sendQueue }}),
		Ctx:           a.ctx,
	})
}

// newMailMergeService builds the host-side merge service from the shared
// compose ops, a draft saver, and the contact store. Shared by App and
// ComposerApp (both carry composeOps + contactStore). failureRecorder
// is resolved lazily (the send queue may not exist yet when the bridge
// is built); nil means Sent-save failures only log, as before.
func newMailMergeService(ops *composeOps, draftSaver hostmm.DraftSaver, contacts *contact.Store, failureRecorder hostmm.SentSaveFailureRecorder) *hostmm.Service {
	return hostmm.NewService(hostmm.ServiceDeps{
		DraftSaver:              draftSaver,
		SenderFactory:           &mailMergeSenderFactory{ops: ops},
		SentSaver:               &mailMergeSentSaver{ops: ops},
		ContactLister:           &mailMergeContactLister{store: contacts},
		MessageSigner:           &mailMergeMessageSigner{ops: ops},
		SentSaveFailureRecorder: failureRecorder,
	})
}

// mailMergeSentSaveFailureRecorder adapts a send queue service to the
// hostmm.SentSaveFailureRecorder interface, resolving the service
// lazily so the bridge can be built before the queue exists.
type mailMergeSentSaveFailureRecorder struct {
	get func() *sendQueueService
}

func (r *mailMergeSentSaveFailureRecorder) RecordSentSaveFailure(_ context.Context, accountID, subject, recipient, fromAddress string, saveErr error) {
	if svc := r.get(); svc != nil {
		svc.RecordMailMergeSentSaveFailure(accountID, subject, recipient, fromAddress, saveErr)
	}
}

// mailMergeMessageSigner adapts composeOps' S/MIME + PGP signers to
// hostmm.MessageSigner (Phase 3, signing only — never encryption).
// Kind resolution mirrors the normal composer
// (Composer.svelte updateSecurityForIdentity): S/MIME first when the
// account has a valid (non-expired) certificate for the sender
// identity (else the account default), else PGP when it has a valid
// key. An identity-specific certificate/key that is expired falls back
// to the account default instead of being used expired — the composer
// hides the sign option for expired identities, and the signers
// themselves do not check expiry.
type mailMergeMessageSigner struct{ ops *composeOps }

// SignMessage signs one already-built RFC822 message with the resolved
// kind and returns the signed bytes + kind used.
func (s *mailMergeMessageSigner) SignMessage(accountID, fromEmail string, rawMsg []byte) ([]byte, string, error) {
	switch s.SigningKind(accountID, fromEmail) {
	case "smime":
		signed, err := s.ops.smimeSigner.SignMessage(accountID, fromEmail, rawMsg)
		if err != nil {
			return nil, "", err
		}
		return signed, "smime", nil
	case "pgp":
		signed, err := s.ops.pgpSigner.SignMessage(accountID, fromEmail, rawMsg)
		if err != nil {
			return nil, "", err
		}
		return signed, "pgp", nil
	default:
		return nil, "", fmt.Errorf("mailmerge: no signing identity configured for this account")
	}
}

// SigningKind reports which signer would be used for the account +
// sender identity ("smime", "pgp", or "" when the account has no
// usable signing identity), without signing anything.
func (s *mailMergeMessageSigner) SigningKind(accountID, fromEmail string) string {
	if s.hasValidSMIMECert(accountID, fromEmail) {
		return "smime"
	}
	if s.hasValidPGPKey(accountID, fromEmail) {
		return "pgp"
	}
	return ""
}

// DefaultSign reports whether the account's composer would sign a new
// message by default: the sign policy for the resolved kind is
// "always" (the same rule Composer.svelte applies on identity load).
func (s *mailMergeMessageSigner) DefaultSign(accountID, fromEmail string) bool {
	switch s.SigningKind(accountID, fromEmail) {
	case "smime":
		policy, err := s.ops.smimeStore.GetSignPolicy(accountID)
		return err == nil && policy == "always"
	case "pgp":
		policy, err := s.ops.pgpStore.GetSignPolicy(accountID)
		return err == nil && policy == "always"
	default:
		return false
	}
}

// hasValidSMIMECert reports whether the account has a valid
// (non-expired) S/MIME certificate for the sender identity, else the
// account default.
func (s *mailMergeMessageSigner) hasValidSMIMECert(accountID, fromEmail string) bool {
	if fromEmail != "" {
		if cert, _, err := s.ops.smimeStore.GetCertificateByEmail(accountID, fromEmail); err == nil && cert != nil && !cert.IsExpired {
			return true
		}
	}
	cert, _, err := s.ops.smimeStore.GetDefaultCertificate(accountID)
	return err == nil && cert != nil && !cert.IsExpired
}

// hasValidPGPKey reports whether the account has a valid
// (non-expired) PGP key for the sender identity, else the account
// default.
func (s *mailMergeMessageSigner) hasValidPGPKey(accountID, fromEmail string) bool {
	if fromEmail != "" {
		if key, _, err := s.ops.pgpStore.GetKeyByEmail(accountID, fromEmail); err == nil && key != nil && !key.IsExpired {
			return true
		}
	}
	key, _, err := s.ops.pgpStore.GetDefaultKey(accountID)
	return err == nil && key != nil && !key.IsExpired
}

// appMailMergeDraftSaver adapts App.SaveDraft to hostmm.DraftSaver: one
// draft per row in "drafts" deliver mode. The draft ID/result is not
// needed by the run, so it is discarded.
type appMailMergeDraftSaver struct{ a *App }

func (s *appMailMergeDraftSaver) SaveDraft(accountID string, msg smtp.ComposeMessage) error {
	_, err := s.a.SaveDraft(accountID, msg, "")
	return err
}

// composerMailMergeDraftSaver adapts ComposerApp.SaveDraft to
// hostmm.DraftSaver (the detached composer's twin of the adapter above).
type composerMailMergeDraftSaver struct{ c *ComposerApp }

func (s *composerMailMergeDraftSaver) SaveDraft(accountID string, msg smtp.ComposeMessage) error {
	_, err := s.c.SaveDraft(accountID, msg, "")
	return err
}

// mailMergeSenderFactory builds the run's ONE reused SMTP connection:
// Connect + Login once, SendMail per row, Close at the end. The client
// config comes from composeOps.smtpClientConfig — the exact config a
// normal send uses (password or OAuth2, shared-mailbox / separate-SMTP
// username overrides, TLS).
type mailMergeSenderFactory struct{ ops *composeOps }

func (f *mailMergeSenderFactory) NewSender(ctx context.Context, accountID string) (hostmm.MailSender, error) {
	acc, err := f.ops.accountStore.Get(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", accountID)
	}
	if acc.NoOutgoingServer {
		return nil, fmt.Errorf("account %q is configured as receive-only (no outgoing server)", acc.Email)
	}
	cfg, err := f.ops.smtpClientConfig(ctx, acc)
	if err != nil {
		return nil, err
	}
	client := smtp.NewClient(cfg)
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	if err := client.Login(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("failed to login to SMTP server: %w", err)
	}
	return &mailMergeSMTPSender{client: client}, nil
}

// mailMergeSMTPSender adapts *smtp.Client to hostmm.MailSender.
type mailMergeSMTPSender struct{ client *smtp.Client }

func (s *mailMergeSMTPSender) SendMail(from string, to []string, msg []byte) error {
	return s.client.SendMail(from, to, msg)
}

func (s *mailMergeSMTPSender) Close() error { return s.client.Close() }

// mailMergeSentSaver mirrors App.SendMessage's Sent handling: skip when
// the provider auto-saves Sent mail, else IMAP APPEND via
// composeOps.saveToSentFolder.
type mailMergeSentSaver struct{ ops *composeOps }

func (s *mailMergeSentSaver) SaveToSent(ctx context.Context, accountID string, rawMsg []byte) error {
	acc, err := s.ops.accountStore.Get(accountID)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}
	if acc == nil {
		return fmt.Errorf("account not found: %s", accountID)
	}
	if providerAutoSavesSentMail(acc.IMAPHost) {
		return nil
	}
	return s.ops.saveToSentFolder(ctx, accountID, acc, rawMsg)
}

// mailMergeContactLister adapts contactStore.ListRecords (the unified
// local + CardDAV + Google + Microsoft set) to hostmm.ContactLister.
// It reads the injected contact store directly — coreapi.Contacts is nil
// in the detached composer process, so it must never be used here.
type mailMergeContactLister struct{ store *contact.Store }

func (l *mailMergeContactLister) ListContacts() ([]map[string]string, []string, error) {
	recs, err := l.store.ListRecords(contact.RecordFilter{})
	if err != nil {
		return nil, nil, err
	}
	rows := make([]map[string]string, 0, len(recs))
	for _, r := range recs {
		if r == nil {
			continue
		}
		rows = append(rows, hostmm.RecordToRow(r))
	}
	return rows, hostmm.ContactColumns(), nil
}
