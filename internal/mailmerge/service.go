package mailmerge

import (
	"context"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/smtp"
)

// DraftSaver is the narrow host-injected draft path: one SaveDraft call
// per row in "drafts" deliver mode. The host adapts App.SaveDraft /
// ComposerApp.SaveDraft (which return draft results the run does not
// need). internal/mailmerge never imports the app package.
type DraftSaver interface {
	SaveDraft(accountID string, msg smtp.ComposeMessage) error
}

// MailSender is one reused SMTP connection for a whole send run:
// Connect + Login happen once (via SenderFactory), SendMail per row,
// Close at the end. Never a fresh connection per message.
type MailSender interface {
	SendMail(from string, to []string, msg []byte) error
	Close() error
}

// SenderFactory builds the run's single SMTP connection for an account
// (password or OAuth2, exactly like the host's normal send path).
type SenderFactory interface {
	NewSender(ctx context.Context, accountID string) (MailSender, error)
}

// SentSaver mirrors the host's save-to-Sent behaviour: after each send,
// save the message to Sent the same way App.SendMessage does (skipped
// entirely when the provider auto-saves Sent mail). A save failure must
// NOT fail the send — the host logs it and moves on.
type SentSaver interface {
	SaveToSent(ctx context.Context, accountID string, rawMsg []byte) error
}

// ContactLister returns the unified contact set (local + CardDAV +
// Google + Microsoft) as merge rows. The host adapts
// contactStore.ListRecords; the service never touches coreapi.Contacts
// (nil in the detached composer process).
type ContactLister interface {
	ListContacts() ([]map[string]string, []string, error)
}

// MessageSigner signs one already-built RFC822 message for an account
// (Phase 3, signing only — never encryption). It resolves S/MIME vs
// PGP the way the normal composer does: S/MIME first when the account
// has a valid (non-expired) certificate for the sender identity (else
// the account default), else PGP when it has a valid key. SignMessage
// returns the signed bytes and the kind actually used ("smime" or
// "pgp"). A nil signer means the host has no signing setup at all —
// signing is then unavailable, never silently skipped.
type MessageSigner interface {
	SignMessage(accountID, fromEmail string, rawMsg []byte) ([]byte, string, error)
	// SigningKind reports which signer would be used for the account +
	// sender identity ("smime", "pgp", or "" when the account has no
	// usable signing identity), without signing anything.
	SigningKind(accountID, fromEmail string) string
	// DefaultSign reports whether the account's composer would sign a
	// new message by default (the S/MIME or PGP sign policy for the
	// resolved kind is "always").
	DefaultSign(accountID, fromEmail string) bool
}

// ServiceDeps bundles the host-provided collaborators. A nil dep fails
// only the operation that needs it (Execute validates the dep for the
// requested deliver mode; ListContacts validates ContactLister).
type ServiceDeps struct {
	DraftSaver    DraftSaver
	SenderFactory SenderFactory
	SentSaver     SentSaver
	ContactLister ContactLister
	// MessageSigner signs per-row messages when the run requests
	// signing. Nil = signing unavailable (Validate/StartRun/Execute
	// all fail a signing run before anything is sent).
	MessageSigner MessageSigner
}

// Service is the host-side run orchestrator. It owns no UI and no
// persistence: the bridge calls Execute in a goroutine, receives one
// RowResult per row via onProgress (for the run log), and persists the
// run. Constructed by the host (App and ComposerApp) and injected into
// the bridge as a plain object — never closures (EXT_RULES R2).
type Service struct {
	deps ServiceDeps
}

// NewService constructs the service. It opens nothing and starts no
// goroutines.
func NewService(deps ServiceDeps) *Service {
	return &Service{deps: deps}
}

// ListContacts returns all unified contacts as merge rows.
func (s *Service) ListContacts() ([]map[string]string, []string, error) {
	if s.deps.ContactLister == nil {
		return nil, nil, fmt.Errorf("mailmerge: no contact lister configured")
	}
	return s.deps.ContactLister.ListContacts()
}

// SigningInfo reports the account's signing setup for the merge
// dialog: whether a usable S/MIME certificate or PGP key exists,
// which kind would be used, and whether the composer's sign policy
// would sign by default. This is the cheap check (no signature is
// created); CheckSigningForRun is the proof that the private key is
// actually usable.
func (s *Service) SigningInfo(accountID, fromEmail string) SigningInfo {
	if s.deps.MessageSigner == nil {
		return SigningInfo{Available: false, Reason: "signing is not available in this build"}
	}
	kind := s.deps.MessageSigner.SigningKind(accountID, fromEmail)
	if kind == "" {
		return SigningInfo{Available: false, Reason: "no S/MIME certificate or PGP key is configured for this account"}
	}
	return SigningInfo{
		Available:   true,
		Kind:        kind,
		DefaultSign: s.deps.MessageSigner.DefaultSign(accountID, fromEmail),
	}
}

// CheckSigningForRun trial-signs a probe message for the run's From
// address (the template From rendered against the first row) and
// returns the kind that would be used ("smime" or "pgp"). It proves
// the account's private key/certificate is actually usable BEFORE
// any row is sent or saved — a missing or unusable key fails here,
// not mid-run. An error means signing is unavailable for this run.
func (s *Service) CheckSigningForRun(req RunRequest) (string, error) {
	if s.deps.MessageSigner == nil {
		return "", fmt.Errorf("no signing identity is configured for this account")
	}
	accountID := req.AccountID
	if accountID == "" {
		accountID = req.Template.AccountID
	}
	if accountID == "" {
		return "", fmt.Errorf("account is required")
	}
	fromEmail := signingFromEmail(req.Template, req.Rows)
	probe := smtp.ComposeMessage{
		From:     smtp.Address{Address: fromEmail},
		To:       []smtp.Address{{Address: fromEmail}},
		Subject:  "mail-merge signing check",
		TextBody: "mail-merge signing check",
	}
	raw, err := probe.ToRFC822()
	if err != nil {
		return "", fmt.Errorf("build signing probe: %w", err)
	}
	_, kind, err := s.deps.MessageSigner.SignMessage(accountID, fromEmail, raw)
	if err != nil {
		return "", fmt.Errorf("signing failed: %w", err)
	}
	return kind, nil
}

// signingFromEmail resolves the sender address a run will sign as:
// the template From rendered against the first row (From may contain
// variables), parsed to a bare address; the raw From string when
// there are no rows or it does not parse. The signer itself falls
// back to the account default when no identity matches, so an
// approximate address here still yields the right kind check.
func signingFromEmail(tmpl Template, rows []map[string]string) string {
	from := strings.TrimSpace(tmpl.From)
	if len(rows) > 0 {
		if rendered, err := Render(tmpl.From, rows[0]); err == nil && strings.TrimSpace(rendered) != "" {
			from = strings.TrimSpace(rendered)
		}
	}
	if parsed, err := mail.ParseAddress(from); err == nil && parsed.Address != "" {
		return parsed.Address
	}
	return from
}

// preflightSign runs the signing pre-flight for a run that requested
// signing and stamps the resolved kind onto the request copy the
// caller keeps using (buildMessage reads it to set the draft sign
// flags). It returns the request to execute. When signing was not
// requested the request is returned unchanged.
func (s *Service) preflightSign(req RunRequest) (RunRequest, error) {
	if !req.Template.Sign {
		return req, nil
	}
	kind, err := s.CheckSigningForRun(req)
	if err != nil {
		return req, fmt.Errorf("mailmerge: signing unavailable: %w", err)
	}
	req.Template.SignKind = kind
	return req, nil
}

// Outcome is the final tally of an Execute call.
type Outcome struct {
	SentCount    int
	FailedCount  int
	SkippedCount int
	HeldCount    int
	QueuedCount  int
	Cancelled    bool
}

// Execute runs a merge over the req's Start/Stop slice. It is
// synchronous — the bridge runs it in a goroutine — and reports every
// row's outcome to onProgress as it happens (nil-safe).
//
//   - Drafts mode: one DraftSaver.SaveDraft per row; the safety cap does
//     NOT apply (draft generation is harmless).
//   - Send mode: ONE reused SMTP connection; after each send the message
//     is saved to Sent via SentSaver (best-effort). The cap applies at
//     SEND time only: once CapLimit rows have been sent, the rest are
//     recorded "held — over cap" and NOT sent.
//   - Cancel (ctx) stops after the current message; unprocessed rows are
//     recorded as skipped ("cancelled before processing").
//   - Per-row failure never stops the run: the error is recorded and the
//     next row is processed (the Thunderbird add-on stops with no record;
//     we do not).
func (s *Service) Execute(ctx context.Context, req RunRequest, onProgress func(RowResult)) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var out Outcome
	emit := func(r RowResult) {
		switch r.Status {
		case RowSent, RowDraftSaved:
			out.SentCount++
		case RowFailed:
			out.FailedCount++
		case RowSkipped:
			out.SkippedCount++
		case RowHeld:
			out.HeldCount++
		}
		if onProgress != nil {
			onProgress(r)
		}
	}

	if req.AccountID == "" {
		return out, fmt.Errorf("mailmerge: account is required")
	}
	switch req.DeliverMode {
	case "drafts":
		if s.deps.DraftSaver == nil {
			return out, fmt.Errorf("mailmerge: no draft saver configured")
		}
	case "send":
		if s.deps.SenderFactory == nil {
			return out, fmt.Errorf("mailmerge: no SMTP sender configured")
		}
	default:
		return out, fmt.Errorf("mailmerge: unknown deliver mode %q", req.DeliverMode)
	}

	// A signing run proves the key is usable BEFORE any row is sent
	// or saved (CheckSigningForRun trial-signs a probe) and stamps
	// the resolved kind onto this request copy for buildMessage's
	// draft flags. A failure here fails the whole run with nothing
	// delivered.
	signedReq, err := s.preflightSign(req)
	if err != nil {
		return out, err
	}
	req = signedReq

	// Global attachments are decoded once; the bytes are shared read-only
	// across rows. A bad base64 payload fails the run before any row.
	globalAtts, err := decodeGlobalAttachments(req.Template.GlobalAttachments)
	if err != nil {
		return out, err
	}

	rows, indices := SliceRows(req.Rows, req.StartRow, req.StopRow)

	var sender MailSender
	if req.DeliverMode == "send" {
		sender, err = s.deps.SenderFactory.NewSender(ctx, req.AccountID)
		if err != nil {
			return out, fmt.Errorf("mailmerge: connect SMTP: %w", err)
		}
		defer func() {
			_ = sender.Close()
		}()
	}

	seenTo := map[string]bool{} // lowercased first-To addresses already sent/saved/attempted
	for _, addr := range req.AlreadySent {
		if norm := strings.ToLower(strings.TrimSpace(addr)); norm != "" {
			seenTo[norm] = true
		}
	}
	sentAttempted := 0 // successful sends, for the cap
	capLimit := req.CapLimit
	if capLimit <= 0 {
		capLimit = 100
	}

	for pos, row := range rows {
		origIndex := indices[pos]
		if ctx.Err() != nil {
			out.Cancelled = true
			emit(RowResult{Index: origIndex, Status: RowSkipped, Error: "cancelled before processing", Timestamp: time.Now()})
			// Mark every remaining row skipped too.
			for j := pos + 1; j < len(rows); j++ {
				emit(RowResult{Index: indices[j], Status: RowSkipped, Error: "cancelled before processing", Timestamp: time.Now()})
			}
			return out, nil
		}

		result := s.processRow(ctx, req, row, origIndex, globalAtts, sender, seenTo, &sentAttempted, capLimit)
		emit(result)

		// Pause between messages (not after the last one), only after a
		// message was actually produced. Skipped/held/failed rows do
		// not consume a pause.
		if pos < len(rows)-1 && (result.Status == RowSent || result.Status == RowDraftSaved) {
			if !s.pause(ctx, req) {
				out.Cancelled = true
				for j := pos + 1; j < len(rows); j++ {
					emit(RowResult{Index: indices[j], Status: RowSkipped, Error: "cancelled before processing", Timestamp: time.Now()})
				}
				return out, nil
			}
		}
	}
	return out, nil
}

// pause sleeps between two messages: exactly PauseSeconds, or a random
// 0–PauseSeconds when RandomPause is set. Returns false when the
// context was cancelled during the pause.
func (s *Service) pause(ctx context.Context, req RunRequest) bool {
	seconds := req.PauseSeconds
	if req.RandomPause {
		if seconds <= 0 {
			return true
		}
		seconds = rand.IntN(seconds + 1) // 0–X inclusive
	}
	if seconds <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Duration(seconds) * time.Second):
		return true
	}
}

// RenderForQueue renders a run for the outbox queue (Phase 2 "Send
// later") instead of delivering it: every accepted row is built into
// its final RFC822 bytes and handed to onQueued, and a RowQueued
// result is reported per row via onProgress. The message is fixed at
// queue time — the queue runner never re-renders.
//
// onQueued reports whether the rendered message was actually
// persisted: a non-nil error fails the row (RowFailed) instead of
// recording it queued, and the address is NOT marked seen — a row
// whose enqueue failed must not block a later row to the same
// address, and the failed row stays resumable via Resume.
//
// Same row semantics as Execute: duplicates suppressed, per-row
// failures recorded without stopping the run, ctx cancel stops after
// the current row (remaining rows skipped). No pause between rows
// (rendering is local work) and no safety cap (the cap governs
// sending, and the queue runner enforces the per-hour budget when
// the messages actually go out).
func (s *Service) RenderForQueue(ctx context.Context, req RunRequest, onQueued func(QueuedMessage) error, onProgress func(RowResult)) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var out Outcome
	emit := func(r RowResult) {
		switch r.Status {
		case RowSent, RowDraftSaved:
			out.SentCount++
		case RowFailed:
			out.FailedCount++
		case RowSkipped:
			out.SkippedCount++
		case RowHeld:
			out.HeldCount++
		case RowQueued:
			out.QueuedCount++
		}
		if onProgress != nil {
			onProgress(r)
		}
	}
	if req.AccountID == "" {
		return out, fmt.Errorf("mailmerge: account is required")
	}
	// Same signing pre-flight as Execute: prove the key is usable and
	// stamp the resolved kind before any row is rendered — the queued
	// bytes are fixed at queue time, so the signature must already be
	// in them, and a failed pre-flight queues nothing.
	signedReq, err := s.preflightSign(req)
	if err != nil {
		return out, err
	}
	req = signedReq
	globalAtts, err := decodeGlobalAttachments(req.Template.GlobalAttachments)
	if err != nil {
		return out, err
	}
	rows, indices := SliceRows(req.Rows, req.StartRow, req.StopRow)
	seenTo := map[string]bool{}
	for _, addr := range req.AlreadySent {
		if norm := strings.ToLower(strings.TrimSpace(addr)); norm != "" {
			seenTo[norm] = true
		}
	}
	for pos, row := range rows {
		origIndex := indices[pos]
		if ctx.Err() != nil {
			out.Cancelled = true
			emit(RowResult{Index: origIndex, Status: RowSkipped, Error: "cancelled before processing", Timestamp: time.Now()})
			for j := pos + 1; j < len(rows); j++ {
				emit(RowResult{Index: indices[j], Status: RowSkipped, Error: "cancelled before processing", Timestamp: time.Now()})
			}
			return out, nil
		}
		now := time.Now()
		bm, bad := s.buildMessage(req, row, origIndex, globalAtts, seenTo)
		if bad != nil {
			emit(*bad)
			continue
		}
		raw, err := bm.msg.ToRFC822()
		if err != nil {
			emit(RowResult{Index: origIndex, Email: bm.email, Status: RowFailed, Error: fmt.Sprintf("build message: %v", err), Timestamp: now, MessageID: bm.msg.MessageID})
			continue
		}
		// Sign at RENDER time: the queued bytes are fixed at queue
		// time, so the signature must already be in them. A signing
		// failure fails this row only — the row is never queued
		// unsigned and the run continues.
		if req.Template.Sign {
			signed, _, signErr := s.deps.MessageSigner.SignMessage(req.AccountID, bm.fromAddr.Address, raw)
			if signErr != nil {
				emit(RowResult{Index: origIndex, Email: bm.email, Status: RowFailed, Error: fmt.Sprintf("sign: %v", signErr), Timestamp: now, MessageID: bm.msg.MessageID})
				continue
			}
			raw = signed
		}
		// The address counts for duplicate suppression only once the
		// row is actually queued: a build/sign/enqueue failure must
		// not poison later rows to the same address (see processRow).
		if onQueued != nil {
			if err := onQueued(QueuedMessage{
				RowIndex: origIndex,
				Email:    bm.email,
				Subject:  bm.msg.Subject,
				From:     bm.fromAddr.Address,
				To:       addressStrings(bm.toAddrs),
				Raw:      raw,
			}); err != nil {
				emit(RowResult{Index: origIndex, Email: bm.email, Status: RowFailed, Error: fmt.Sprintf("enqueue: %v", err), Timestamp: now, MessageID: bm.msg.MessageID})
				continue
			}
		}
		seenTo[strings.ToLower(strings.TrimSpace(bm.email))] = true
		emit(RowResult{Index: origIndex, Email: bm.email, Status: RowQueued, Timestamp: now, MessageID: bm.msg.MessageID})
	}
	return out, nil
}

// RawSender is one reused SMTP connection for sending already-rendered
// queue bytes: Connect + Login happen once (via SenderFactory, exactly
// like a normal send run), Send per queued item, Close at the end. The
// queue runner opens one RawSender per account per dispatch tick, so a
// batch still uses one connection per account — the same reuse as a
// normal send run, and the same SentSaver as a normal send.
type RawSender struct {
	sender    MailSender
	saver     SentSaver
	accountID string
}

// OpenRawSender opens the run's single SMTP connection for an account.
func (s *Service) OpenRawSender(ctx context.Context, accountID string) (*RawSender, error) {
	if s.deps.SenderFactory == nil {
		return nil, fmt.Errorf("mailmerge: no SMTP sender configured")
	}
	sender, err := s.deps.SenderFactory.NewSender(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("mailmerge: connect SMTP: %w", err)
	}
	return &RawSender{sender: sender, saver: s.deps.SentSaver, accountID: accountID}, nil
}

// Send transmits one already-rendered queued message, then saves it to
// Sent the same way a normal send does. afterSMTP (nil-safe) runs the
// moment SMTP accepts the mail, BEFORE the Sent save — the same hook
// pattern the normal queue uses (app/sendqueue.go): the caller clears
// the queue row there, because a crash during the (slow, network)
// Sent save must not leave a row the stale-claim reset re-queues and
// re-sends. A Sent-save failure must NOT fail the send (the message
// is already out).
func (rs *RawSender) Send(ctx context.Context, m QueuedMessage, afterSMTP func()) error {
	if err := rs.sender.SendMail(m.From, m.To, m.Raw); err != nil {
		return err
	}
	// SMTP accepted the mail: record that durably before any
	// post-send work (the Sent save below). See sendMessageWithHook's
	// doc comment in app/compose.go.
	if afterSMTP != nil {
		afterSMTP()
	}
	if rs.saver != nil {
		if err := rs.saver.SaveToSent(ctx, rs.accountID, m.Raw); err != nil {
			slog := logging.WithComponent("mailmerge")
			slog.Warn().Err(err).Str("email", m.Email).Msg("Failed to save queued sent message to Sent folder")
		}
	}
	return nil
}

// Close closes the SMTP connection.
func (rs *RawSender) Close() error { return rs.sender.Close() }

// addressStrings returns the bare addresses of a parsed address list
// (the envelope recipients for SendMail).
func addressStrings(addrs []mail.Address) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Address)
	}
	return out
}

// builtMessage is one row fully rendered and composed, plus the parsed
// sender/recipients the delivery paths need.
type builtMessage struct {
	msg      smtp.ComposeMessage
	fromAddr mail.Address
	toAddrs  []mail.Address
	email    string // first To address
}

// buildMessage renders one row and builds its ComposeMessage. It
// returns a non-nil *RowResult (skip/fail, Index/Email filled) when
// the row must not be delivered, and nil otherwise. Shared by
// processRow (drafts/send) and RenderForQueue ("Send later") so all
// three deliver modes render identically.
func (s *Service) buildMessage(req RunRequest, row map[string]string, origIndex int, globalAtts []smtp.Attachment, seenTo map[string]bool) (*builtMessage, *RowResult) {
	now := time.Now()
	fail := func(err string) *RowResult {
		return &RowResult{Index: origIndex, Status: RowFailed, Error: err, Timestamp: now}
	}
	skip := func(email, err string) *RowResult {
		return &RowResult{Index: origIndex, Email: email, Status: RowSkipped, Error: err, Timestamp: now}
	}

	// Render the template. A missing variable fails the row (the bridge
	// blocks the run on unresolved variables first, so this is the
	// defence-in-depth path).
	rendered, err := RenderRow(req.Template, row, req.AttachmentDir, origIndex)
	if err != nil {
		if names := missingErrorNames(err); len(names) > 0 {
			return nil, fail(fmt.Sprintf("unresolved variable(s): %s", strings.Join(names, ", ")))
		}
		return nil, fail(err.Error())
	}

	// From: required, must parse.
	fromRendered, err := Render(req.Template.From, row)
	if err != nil {
		if names := missingErrorNames(err); len(names) > 0 {
			return nil, fail(fmt.Sprintf("unresolved variable(s): %s", strings.Join(names, ", ")))
		}
		return nil, fail(err.Error())
	}
	fromAddr, err := mail.ParseAddress(strings.TrimSpace(fromRendered))
	if err != nil || strings.TrimSpace(fromRendered) == "" {
		return nil, fail(fmt.Sprintf("invalid From address %q", fromRendered))
	}

	// To: empty, still-templated, unparseable or invalid addresses are
	// skipped and logged — never sent.
	toStr := strings.TrimSpace(rendered.To)
	if toStr == "" {
		return nil, skip("", "empty To address")
	}
	if strings.Contains(toStr, "{{") {
		return nil, skip("", fmt.Sprintf("To still contains an unresolved variable: %q", toStr))
	}
	toAddrs, err := parseAddressList(toStr)
	if err != nil || len(toAddrs) == 0 {
		return nil, skip("", fmt.Sprintf("invalid To address %q", toStr))
	}
	for _, a := range toAddrs {
		if !validEmail(a.Address) {
			return nil, skip("", fmt.Sprintf("invalid To address %q", a.Address))
		}
	}
	email := toAddrs[0].Address
	dupKey := strings.ToLower(strings.TrimSpace(email))
	if seenTo[dupKey] {
		return nil, skip(email, "duplicate recipient — already sent in this run")
	}

	// Cc / Bcc: invalid entries are dropped, the row still goes out.
	// ReplyTo: a single address; invalid means no Reply-To, not a failure.
	ccAddrs := parseLenient(rendered.Cc)
	bccAddrs := parseLenient(rendered.Bcc)
	var replyTo *smtp.Address
	if rt, err := Render(req.Template.ReplyTo, row); err != nil {
		if names := missingErrorNames(err); len(names) > 0 {
			return nil, fail(fmt.Sprintf("unresolved variable(s): %s", strings.Join(names, ", ")))
		}
		return nil, fail(err.Error())
	} else if strings.TrimSpace(rt) != "" {
		if parsed, perr := mail.ParseAddress(strings.TrimSpace(rt)); perr == nil && validEmail(parsed.Address) {
			replyTo = &smtp.Address{Name: parsed.Name, Address: parsed.Address}
		}
	}

	// Per-row attachments: read from disk per row (never hold all
	// rendered messages in memory). A missing file skips the row.
	rowAtts, skipped := s.resolveRowAttachments(req, row)
	if skipped != nil {
		skipped.Index = origIndex
		skipped.Email = email
		return nil, skipped
	}

	msg := smtp.ComposeMessage{
		From:        smtp.Address{Name: fromAddr.Name, Address: fromAddr.Address},
		To:          toSMTP(toAddrs),
		Cc:          toSMTP(ccAddrs),
		Bcc:         toSMTP(bccAddrs),
		ReplyTo:     replyTo,
		Subject:     rendered.Subject,
		Attachments: append(append([]smtp.Attachment{}, globalAtts...), rowAtts...),
	}
	// One Message-ID per row, assigned here at render time: the
	// identity of this row's mail is fixed before anything is sent,
	// so a retry after a lost SMTP acknowledgement rebuilds the same
	// mail with the same Message-ID instead of minting a fresh one
	// the server cannot recognise as a duplicate. A resumed run (or
	// a held-row batch) carries the ID assigned on the first attempt
	// in req.MessageIDs; a fresh render mints one, exactly as
	// QueueSend does for normal sends.
	if id := messageIDForRow(req, origIndex); id != "" {
		msg.MessageID = id
	} else {
		msg.MessageID = smtp.NewMessageID()
	}
	if req.Template.IsPlainText {
		msg.TextBody = rendered.BodyText
	} else {
		msg.TextBody = rendered.BodyText
		msg.HTMLBody = rendered.BodyHTML
	}
	// Signing intent for the drafts path: the draft model already
	// carries these flags (draft.SignMessage / draft.PGPSignMessage),
	// so SaveDraft preserves them and sending the draft later signs
	// it — the intent is never silently dropped. The kind is the one
	// the run pre-flight resolved (S/MIME and PGP are mutually
	// exclusive, exactly like the composer's toggles). The IMAP copy
	// of the draft is signed by the host's draft sync, like a normal
	// signed draft.
	if req.Template.Sign {
		switch req.Template.SignKind {
		case "smime":
			msg.SignMessage = true
		case "pgp":
			msg.PGPSignMessage = true
		}
	}
	return &builtMessage{msg: msg, fromAddr: *fromAddr, toAddrs: toAddrs, email: email}, nil
}

// messageIDForRow returns the Message-ID a previous attempt assigned
// to the row at this position in req.Rows ("" when none was assigned
// — a fresh render mints one in buildMessage). Out-of-range lookups
// (a stale MessageIDs slice from an older stored request) are "".
func messageIDForRow(req RunRequest, index int) string {
	if index >= 0 && index < len(req.MessageIDs) {
		return strings.TrimSpace(req.MessageIDs[index])
	}
	return ""
}

// processRow renders, validates and delivers one row, returning its
// RowResult. seenTo/sentAttempted are updated by the caller-visible
// side effects (duplicate suppression, cap counting).
func (s *Service) processRow(ctx context.Context, req RunRequest, row map[string]string, origIndex int, globalAtts []smtp.Attachment, sender MailSender, seenTo map[string]bool, sentAttempted *int, capLimit int) RowResult {
	now := time.Now()

	bm, bad := s.buildMessage(req, row, origIndex, globalAtts, seenTo)
	if bad != nil {
		return *bad
	}
	dupKey := strings.ToLower(strings.TrimSpace(bm.email))
	email := bm.email
	// The Message-ID assigned at render time rides on every outcome
	// of this row — sent, held, or failed — so the run log persists
	// it and a resumed run rebuilds the same mail with the same
	// identity.
	messageID := bm.msg.MessageID
	fail := func(err string) RowResult {
		return RowResult{Index: origIndex, Email: email, Status: RowFailed, Error: err, Timestamp: now, MessageID: messageID}
	}

	// Duplicate suppression marks an address ONLY once its row has
	// actually succeeded (draft saved / message sent). Marking before
	// delivery poisoned the suppression: a row that failed mid-send
	// (build, sign, transmit) left the address in seenTo, so later
	// rows to the same address were skipped as "duplicate recipient
	// — already sent in this run" and the failed row was the only
	// record — unresumable except by re-running the whole merge.
	// A failed row stays unmarked, so a later row to the same address
	// is delivered and the failed row is re-runnable via Resume.
	// Held rows do NOT mark the address — both copies stay held and
	// the duplicate is caught when the batch actually sends.
	markSeen := func() { seenTo[dupKey] = true }

	switch req.DeliverMode {
	case "drafts":
		if err := s.deps.DraftSaver.SaveDraft(req.AccountID, bm.msg); err != nil {
			return fail(fmt.Sprintf("save draft: %v", err))
		}
		markSeen()
		return RowResult{Index: origIndex, Email: email, Status: RowDraftSaved, Timestamp: now, MessageID: messageID}
	case "send":
		if req.CapEnabled && *sentAttempted >= capLimit {
			return RowResult{Index: origIndex, Email: email, Status: RowHeld, Error: "held — over cap", Timestamp: now, MessageID: messageID}
		}
		raw, err := bm.msg.ToRFC822()
		if err != nil {
			return fail(fmt.Sprintf("build message: %v", err))
		}
		// Sign at SEND time, per row: the body differs per recipient,
		// so each row's personalised bytes are signed individually
		// (the same order as composeOps.sendMessage: build, then sign,
		// then transmit). A signing failure fails this row only — the
		// row is never sent unsigned and the run continues.
		if req.Template.Sign {
			signed, _, signErr := s.deps.MessageSigner.SignMessage(req.AccountID, bm.fromAddr.Address, raw)
			if signErr != nil {
				return fail(fmt.Sprintf("sign: %v", signErr))
			}
			raw = signed
		}
		if err := sender.SendMail(bm.fromAddr.Address, bm.msg.AllRecipients(), raw); err != nil {
			return fail(fmt.Sprintf("send: %v", err))
		}
		markSeen()
		*sentAttempted++
		// Save to Sent the way App.SendMessage does. A failure here
		// must not fail the send (the message is already out).
		if s.deps.SentSaver != nil {
			if err := s.deps.SentSaver.SaveToSent(ctx, req.AccountID, raw); err != nil {
				slog := logging.WithComponent("mailmerge")
				slog.Warn().Err(err).Str("email", email).Msg("Failed to save sent message to Sent folder")
			}
		}
		return RowResult{Index: origIndex, Email: email, Status: RowSent, Timestamp: now, MessageID: messageID}
	default:
		return fail(fmt.Sprintf("unknown deliver mode %q", req.DeliverMode))
	}
}

// resolveRowAttachments renders the per-row attachment field and reads
// each file from disk. A missing/unreadable file skips the row (the
// returned RowResult is non-nil in that case; the caller fills in its
// Index/Email).
func (s *Service) resolveRowAttachments(req RunRequest, row map[string]string) ([]smtp.Attachment, *RowResult) {
	paths := ResolveAttachmentPaths(req.Template.AttachmentField, row, req.AttachmentDir)
	if len(paths) == 0 {
		return nil, nil
	}
	atts := make([]smtp.Attachment, 0, len(paths))
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err != nil {
			skipped := RowResult{Status: RowSkipped, Error: fmt.Sprintf("attachment file not found: %s", p), Timestamp: time.Now()}
			return nil, &skipped
		}
		ct := mime.TypeByExtension(filepath.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		atts = append(atts, smtp.Attachment{
			Filename:    filepath.Base(p),
			ContentType: ct,
			Content:     content,
		})
	}
	return atts, nil
}

// parseLenient parses an address list, dropping entries that do not
// parse or are not valid emails. Never errors.
func parseLenient(s string) []mail.Address {
	addrs, err := parseAddressList(s)
	if err != nil {
		return nil
	}
	out := addrs[:0]
	for _, a := range addrs {
		if validEmail(a.Address) {
			out = append(out, a)
		}
	}
	return out
}

func toSMTP(addrs []mail.Address) []smtp.Address {
	out := make([]smtp.Address, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, smtp.Address{Name: a.Name, Address: a.Address})
	}
	return out
}

// decodeGlobalAttachments base64-decodes the template's global
// attachments once per run.
func decodeGlobalAttachments(in []GlobalAttachment) ([]smtp.Attachment, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]smtp.Attachment, 0, len(in))
	for _, g := range in {
		var content []byte
		if g.DataBase64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(g.DataBase64)
			if err != nil {
				return nil, fmt.Errorf("mailmerge: global attachment %q: invalid base64: %w", g.Filename, err)
			}
			content = decoded
		}
		ct := g.ContentType
		if ct == "" {
			ct = mime.TypeByExtension(filepath.Ext(g.Filename))
			if ct == "" {
				ct = "application/octet-stream"
			}
		}
		out = append(out, smtp.Attachment{
			Filename:    g.Filename,
			ContentType: ct,
			Content:     content,
		})
	}
	return out, nil
}
