package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostmm "github.com/absonsnet/daak/internal/mailmerge"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/absonsnet/daak/internal/smtp"
)

type fakeSettingsStore struct{ enabled bool }

func (f *fakeSettingsStore) IsExtensionEnabled(_ string) (bool, error) {
	return f.enabled, nil
}

type fakeDraftSaver struct {
	saved []smtp.ComposeMessage
}

func (f *fakeDraftSaver) SaveDraft(_ string, msg smtp.ComposeMessage) error {
	f.saved = append(f.saved, msg)
	return nil
}

type fakeSender struct {
	sent []string
	// sentRaw records the exact bytes of every transmission that
	// reached the (fake) server, including ones the client then
	// treats as failed — the lost-acknowledgement case.
	sentRaw []string
	fail    map[string]error
	// failOnce fails the next send to this address only, then
	// succeeds: a transient mid-send failure for one row.
	failOnce map[string]error
	// failAfterSend, when set for a recipient, records the bytes and
	// then returns the error: the server accepted DATA but the ack
	// was lost, so the worker must retry with identical bytes.
	failAfterSend map[string]error
}

func (f *fakeSender) SendMail(_ string, to []string, raw []byte) error {
	for _, rcpt := range to {
		if err, ok := f.fail[rcpt]; ok {
			return err
		}
		if err, ok := f.failOnce[rcpt]; ok {
			delete(f.failOnce, rcpt)
			return err
		}
	}
	f.sent = append(f.sent, to...)
	f.sentRaw = append(f.sentRaw, string(raw))
	for _, rcpt := range to {
		if err, ok := f.failAfterSend[rcpt]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeSender) Close() error { return nil }

type fakeSenderFactory struct {
	sender  *fakeSender
	created int
	// createErr fails NewSender (e.g. SMTP connect failure) when set.
	createErr error
	// onCreate, when set, runs inside NewSender before it returns —
	// a test uses it to synchronise two concurrent dispatches.
	onCreate func()
}

func (f *fakeSenderFactory) NewSender(_ context.Context, _ string) (hostmm.MailSender, error) {
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created++
	return f.sender, nil
}

type fakeSentSaver struct {
	saved int
	// onSave, when set, runs inside SaveToSent — a test observes
	// state at the exact moment the Sent save runs.
	onSave func()
}

func (f *fakeSentSaver) SaveToSent(_ context.Context, _ string, _ []byte) error {
	f.saved++
	if f.onSave != nil {
		f.onSave()
	}
	return nil
}

type fakeContactLister struct{}

func (f *fakeContactLister) ListContacts() ([]map[string]string, []string, error) {
	return []map[string]string{
		{"DisplayName": "John Doe", "PrimaryEmail": "john@example.com"},
		{"DisplayName": "Jane Smith", "PrimaryEmail": "jane@example.com"},
	}, []string{"DisplayName", "PrimaryEmail"}, nil
}

type bridgeTestEnv struct {
	bridge  *MailMergeBridge
	saver   *fakeDraftSaver
	sender  *fakeSender
	factory *fakeSenderFactory
	sent    *fakeSentSaver
}

func newBridgeTestEnv(t *testing.T) *bridgeTestEnv {
	return newBridgeTestEnvWithSigner(t, nil)
}

func newBridgeTestEnvWithSigner(t *testing.T, signer hostmm.MessageSigner) *bridgeTestEnv {
	t.Helper()
	dir := t.TempDir()
	saver := &fakeDraftSaver{}
	sender := &fakeSender{fail: map[string]error{}, failAfterSend: map[string]error{}}
	factory := &fakeSenderFactory{sender: sender}
	sent := &fakeSentSaver{}
	svc := hostmm.NewService(hostmm.ServiceDeps{
		DraftSaver:    saver,
		SenderFactory: factory,
		SentSaver:     sent,
		ContactLister: &fakeContactLister{},
		MessageSigner: signer,
	})
	b := NewMailMergeBridge(MailMergeBridgeDeps{
		SettingsStore: &fakeSettingsStore{enabled: true},
		Paths:         &platform.Paths{Data: dir},
		Service:       svc,
	})
	return &bridgeTestEnv{bridge: b, saver: saver, sender: sender, factory: factory, sent: sent}
}

// fakeMessageSigner is a no-op signer for bridge tests: it returns the
// raw bytes unchanged (the real signers are proven in
// internal/mailmerge/signing_test.go). fail makes every sign fail.
type fakeMessageSigner struct {
	kind string
	fail error
}

func (f *fakeMessageSigner) SignMessage(_, _ string, raw []byte) ([]byte, string, error) {
	if f.fail != nil {
		return nil, "", f.fail
	}
	return raw, f.kind, nil
}

func (f *fakeMessageSigner) SigningKind(_, _ string) string { return f.kind }
func (f *fakeMessageSigner) DefaultSign(_, _ string) bool   { return f.kind == "smime" }

func testBridgeTemplate() hostmm.Template {
	return hostmm.Template{
		AccountID: "acc1",
		From:      "Sender <sender@example.com>",
		To:        "{{FirstName}} {{LastName}} <{{Email}}>",
		Subject:   "Hello {{FirstName}}",
		BodyHTML:  "<p>Hi {{FirstName}}.</p>",
		BodyText:  "Hi {{FirstName}}.",
	}
}

func testBridgeRows() []map[string]string {
	return []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com"},
		{"FirstName": "Bob", "LastName": "Lee", "Email": "bob@example.com"},
	}
}

// waitForRun polls GetRun until the run reaches a terminal status.
func waitForRun(t *testing.T, b *MailMergeBridge, runID string) hostmm.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := b.MailMerge_GetRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		switch run.Status {
		case hostmm.RunDone, hostmm.RunCancelled, hostmm.RunHeld, hostmm.RunFailed, hostmm.RunQueued:
			return run
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish in time", runID)
	return hostmm.Run{}
}

func TestBridgeParseFile(t *testing.T) {
	env := newBridgeTestEnv(t)
	csvPath := filepath.Join(t.TempDir(), "list.csv")
	if err := os.WriteFile(csvPath, []byte("FirstName,Email\nJohn,john@example.com\nJane,jane@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := env.bridge.MailMerge_ParseFile(csvPath, "csv")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RowCount != 2 || len(parsed.Columns) != 2 || len(parsed.PreviewRows) != 2 {
		t.Fatalf("parsed %+v", parsed)
	}
	jsonPath := filepath.Join(t.TempDir(), "list.json")
	if err := os.WriteFile(jsonPath, []byte(`[["Email"],["a@example.com"],["b@example.com"]]`), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err = env.bridge.MailMerge_ParseFile(jsonPath, "json")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RowCount != 2 {
		t.Fatalf("json row count %d", parsed.RowCount)
	}
	if _, err := env.bridge.MailMerge_ParseFile(csvPath, "xlsx"); err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if _, err := env.bridge.MailMerge_ParseFile("/nonexistent.csv", "csv"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestBridgeGetContacts(t *testing.T) {
	env := newBridgeTestEnv(t)
	res, err := env.bridge.MailMerge_GetContacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || len(res.Columns) != 2 || res.Rows[0]["PrimaryEmail"] != "john@example.com" {
		t.Fatalf("contacts %+v", res)
	}
}

func TestBridgePreviewAndValidate(t *testing.T) {
	env := newBridgeTestEnv(t)
	msgs, err := env.bridge.MailMerge_Preview(testBridgeTemplate(), testBridgeRows(), []int{0, 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].To != "John Doe <john@example.com>" || msgs[1].Subject != "Hello Bob" {
		t.Fatalf("preview %+v", msgs)
	}
	// Empty indices = first 3 rows.
	msgs, err = env.bridge.MailMerge_Preview(testBridgeTemplate(), testBridgeRows(), nil)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("default preview %d err %v", len(msgs), err)
	}
	v, err := env.bridge.MailMerge_Validate(testBridgeTemplate(), testBridgeRows())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.UnresolvedVariables) != 0 || v.DuplicateEmails != 0 {
		t.Fatalf("validate %+v", v)
	}
	bad := testBridgeTemplate()
	bad.Subject = "{{Nope}}"
	v, err = env.bridge.MailMerge_Validate(bad, testBridgeRows())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.UnresolvedVariables) != 1 || v.UnresolvedVariables[0] != "Nope" {
		t.Fatalf("validate bad %+v", v)
	}
}

func TestBridgeStartRunDrafts(t *testing.T) {
	env := newBridgeTestEnv(t)
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runID == "" {
		t.Fatal("empty run ID")
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunDone || run.SentCount != 3 || len(run.Rows) != 3 {
		t.Fatalf("run %+v", run)
	}
	if len(env.saver.saved) != 3 {
		t.Fatalf("drafts saved %d", len(env.saver.saved))
	}
	runs, err := env.bridge.MailMerge_ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != runID {
		t.Fatalf("list %+v", runs)
	}
}

func TestBridgeStartRunBlockedOnUnresolved(t *testing.T) {
	env := newBridgeTestEnv(t)
	tmpl := testBridgeTemplate()
	tmpl.Subject = "Hi {{Missing}}"
	if _, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: tmpl, Rows: testBridgeRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	}); err == nil {
		t.Fatal("expected unresolved-variable error")
	}
	if len(env.saver.saved) != 0 {
		t.Fatal("no drafts must be saved on blocked run")
	}
}

// Signing requested but the account has no usable identity must be
// visible in Validate (SigningError) and must block StartRun — before
// anything is saved or sent.
func TestBridgeValidateAndStartRunBlockedOnSigning(t *testing.T) {
	env := newBridgeTestEnv(t) // no MessageSigner: nothing configured
	tmpl := testBridgeTemplate()
	tmpl.Sign = true
	v, err := env.bridge.MailMerge_Validate(tmpl, testBridgeRows())
	if err != nil {
		t.Fatal(err)
	}
	if v.SigningError == "" {
		t.Fatalf("validate %+v: expected SigningError", v)
	}
	if _, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: tmpl, Rows: testBridgeRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	}); err == nil {
		t.Fatal("expected signing-unavailable error")
	}
	if len(env.saver.saved) != 0 {
		t.Fatal("no drafts must be saved on blocked run")
	}

	// A signer that always fails is the same failure, surfaced the
	// same way (the private key is unusable, not merely unconfigured).
	env = newBridgeTestEnvWithSigner(t, &fakeMessageSigner{kind: "smime", fail: errors.New("key unavailable")})
	v, err = env.bridge.MailMerge_Validate(tmpl, testBridgeRows())
	if err != nil {
		t.Fatal(err)
	}
	if v.SigningError == "" {
		t.Fatalf("validate %+v: expected SigningError for failing signer", v)
	}
}

// With a usable signer, Validate reports no signing error and a
// signing drafts run completes with the intent preserved on the
// drafts (SignMessage set for S/MIME).
func TestBridgeStartRunSignedDrafts(t *testing.T) {
	env := newBridgeTestEnvWithSigner(t, &fakeMessageSigner{kind: "smime"})
	tmpl := testBridgeTemplate()
	tmpl.Sign = true
	v, err := env.bridge.MailMerge_Validate(tmpl, testBridgeRows())
	if err != nil {
		t.Fatal(err)
	}
	if v.SigningError != "" {
		t.Fatalf("validate SigningError %q", v.SigningError)
	}
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: tmpl, Rows: testBridgeRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunDone || run.SentCount != 3 {
		t.Fatalf("run %+v", run)
	}
	if len(env.saver.saved) != 3 {
		t.Fatalf("drafts saved %d", len(env.saver.saved))
	}
	for i, m := range env.saver.saved {
		if !m.SignMessage || m.PGPSignMessage {
			t.Fatalf("draft %d: SignMessage=%v PGPSignMessage=%v", i, m.SignMessage, m.PGPSignMessage)
		}
	}
}

func TestBridgeGetSigningInfo(t *testing.T) {
	env := newBridgeTestEnv(t) // no signer
	info, err := env.bridge.MailMerge_GetSigningInfo("acc1", "sender@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if info.Available || info.Reason == "" {
		t.Fatalf("info %+v: want unavailable with a reason", info)
	}

	env = newBridgeTestEnvWithSigner(t, &fakeMessageSigner{kind: "pgp"})
	info, err = env.bridge.MailMerge_GetSigningInfo("acc1", "sender@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Available || info.Kind != "pgp" {
		t.Fatalf("info %+v: want available pgp", info)
	}
}

func TestBridgeCapBatches(t *testing.T) {
	env := newBridgeTestEnv(t)
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
		CapEnabled: true, CapLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunHeld || run.SentCount != 2 || run.HeldCount != 1 {
		t.Fatalf("cap run %+v", run)
	}
	if len(env.sender.sent) != 2 {
		t.Fatalf("sent %v", env.sender.sent)
	}
	// Next batch: the per-run cap is already spent (finding 27), so
	// the batch sends nothing — the held row stays held. Only an
	// explicit Send All can exceed the cap.
	if err := env.bridge.MailMerge_SendNextBatch(runID); err != nil {
		t.Fatal(err)
	}
	run = waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunHeld || run.SentCount != 2 || run.HeldCount != 1 {
		t.Fatalf("after next batch %+v", run)
	}
	if len(env.sender.sent) != 2 {
		t.Fatalf("sent %v after capped batch, want still 2", env.sender.sent)
	}
	// Send all remaining: the last one, run done.
	if err := env.bridge.MailMerge_SendAllRemaining(runID); err != nil {
		t.Fatal(err)
	}
	run = waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunDone || run.SentCount != 3 || run.HeldCount != 0 {
		t.Fatalf("after send-all %+v", run)
	}
	if len(env.sender.sent) != 3 {
		t.Fatalf("sent %v", env.sender.sent)
	}
	// No held rows left: further batch calls error.
	if err := env.bridge.MailMerge_SendNextBatch(runID); err == nil {
		t.Fatal("expected no-held-rows error")
	}
}

// TestBridgeResumeRespectsRunCap (audit4 finding 27): a resumed run
// inherits the original run's remaining per-run cap, not a fresh one.
func TestBridgeResumeRespectsRunCap(t *testing.T) {
	env := newBridgeTestEnv(t)
	env.sender.fail["jane@example.com"] = errors.New("452 too many emails")
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
		CapEnabled: true, CapLimit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.SentCount != 2 || run.FailedCount != 1 || run.HeldCount != 0 {
		t.Fatalf("cap run %+v", run)
	}
	// Fix the failure and resume: the failed row re-runs, but the
	// per-run cap is already spent (2 sent of 2), so it is held
	// instead of sent a second time over the cap.
	delete(env.sender.fail, "jane@example.com")
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunHeld || newRun.SentCount != 0 || newRun.HeldCount != 1 {
		t.Fatalf("resumed run %+v, want held with 0 sent (cap spent)", newRun)
	}
	if len(env.sender.sent) != 2 {
		t.Fatalf("sent %v after resume, want still 2 — the cap must not reset", env.sender.sent)
	}
	// The resumed run's stored request carries the cap seed, so a
	// further resume keeps the same budget.
	req, err := env.bridge.store.GetRunRequest(newID)
	if err != nil {
		t.Fatal(err)
	}
	if req.CapLimit != 2 || req.CapAlreadySent != 2 {
		t.Fatalf("resumed run stored request: CapLimit = %d, CapAlreadySent = %d, want 2 and 2 (cap 2, lineage sent 2)", req.CapLimit, req.CapAlreadySent)
	}
}

func TestBridgeCancelRemaining(t *testing.T) {
	env := newBridgeTestEnv(t)
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
		CapEnabled: true, CapLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.HeldCount != 2 {
		t.Fatalf("held %d", run.HeldCount)
	}
	if err := env.bridge.MailMerge_CancelRemaining(runID); err != nil {
		t.Fatal(err)
	}
	run, err = env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != hostmm.RunCancelled || run.HeldCount != 0 || run.SkippedCount != 2 || run.SentCount != 1 {
		t.Fatalf("after cancel-remaining %+v", run)
	}
	if len(env.sender.sent) != 1 {
		t.Fatalf("sent %v", env.sender.sent)
	}
}

func TestBridgeResumeFailedOnly(t *testing.T) {
	env := newBridgeTestEnv(t)
	env.sender.fail["jane@example.com"] = errors.New("452 too many emails")
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.SentCount != 2 || run.FailedCount != 1 {
		t.Fatalf("run %+v", run)
	}
	// Fix the failure, resume: only the failed row re-runs.
	delete(env.sender.fail, "jane@example.com")
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if newID == runID {
		t.Fatal("resume must create a new run")
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunDone || newRun.TotalRows != 1 || newRun.SentCount != 1 {
		t.Fatalf("resumed run %+v", newRun)
	}
	if len(env.sender.sent) != 3 {
		t.Fatalf("sent %v", env.sender.sent)
	}
	// Original run is untouched by the resume.
	orig, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if orig.FailedCount != 1 || orig.SentCount != 2 {
		t.Fatalf("original run changed: %+v", orig)
	}
	// Resuming a run with no failed rows errors.
	if _, err := env.bridge.MailMerge_ResumeRun(newID); err == nil {
		t.Fatal("expected no-failed-rows error")
	}
}

// TestBridgeResumeReusesMessageID is the re-audit R3 end-to-end
// test: a direct-send row whose SMTP acknowledgement is lost is
// recorded failed with the Message-ID assigned at render time
// (persisted in the run log), and ResumeRun rebuilds the same mail
// with that same Message-ID — the server sees one identity for both
// transmissions, not two different mails.
func TestBridgeResumeReusesMessageID(t *testing.T) {
	env := newBridgeTestEnv(t)
	env.sender.failAfterSend["john@example.com"] = errors.New("read tcp: i/o timeout")
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows()[:1],
		DeliverMode: "send", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.FailedCount != 1 || len(run.Rows) != 1 || run.Rows[0].Status != hostmm.RowFailed {
		t.Fatalf("run %+v, want 1 failed row", run)
	}
	firstID := run.Rows[0].MessageID
	if firstID == "" {
		t.Fatal("failed row must persist the Message-ID assigned at render time")
	}
	if len(env.sender.sentRaw) != 1 || queueMessageID(env.sender.sentRaw[0]) != firstID {
		t.Fatalf("first transmission must carry %q: %q", firstID, env.sender.sentRaw)
	}

	// Fix the connection and resume: the retry must reuse the ID.
	delete(env.sender.failAfterSend, "john@example.com")
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunDone || newRun.SentCount != 1 {
		t.Fatalf("resumed run %+v, want done with 1 sent", newRun)
	}
	if len(env.sender.sentRaw) != 2 || queueMessageID(env.sender.sentRaw[1]) != firstID {
		t.Fatalf("resumed transmission must carry %q: %q", firstID, env.sender.sentRaw)
	}
	if len(newRun.Rows) != 1 || newRun.Rows[0].MessageID != firstID {
		t.Fatalf("resumed run row %+v, want the same Message-ID %q", newRun.Rows, firstID)
	}
}

// TestBridgeWholeRunFailureIsFailedAndResumable is the finding-16
// regression test: a run that fails before any row is sent (SMTP
// connect failure) ends in the distinct "failed" status — not
// "cancelled" — with the error recorded and every unprocessed row
// logged failed, so ResumeRun can re-run it.
func TestBridgeWholeRunFailureIsFailedAndResumable(t *testing.T) {
	env := newBridgeTestEnv(t)
	env.factory.createErr = errors.New("dial tcp: connection refused")
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunFailed {
		t.Fatalf("run status %q, want failed", run.Status)
	}
	if run.Error == "" || !strings.Contains(run.Error, "connection refused") {
		t.Fatalf("run error %q, want the connect error recorded", run.Error)
	}
	if run.FailedCount != 3 || len(run.Rows) != 3 || run.SentCount != 0 {
		t.Fatalf("run %+v, want 3 failed rows, 0 sent", run)
	}
	for i, r := range run.Rows {
		if r.Status != hostmm.RowFailed || r.Index != i || !strings.Contains(r.Error, "connection refused") {
			t.Fatalf("row %d %+v, want failed with the connect error", i, r)
		}
	}
	if len(env.sender.sent) != 0 {
		t.Fatalf("nothing must be sent: %v", env.sender.sent)
	}
	// Fix the connection and resume: all three rows re-run and send.
	env.factory.createErr = nil
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if newID == runID {
		t.Fatal("resume must create a new run")
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunDone || newRun.SentCount != 3 || newRun.TotalRows != 3 {
		t.Fatalf("resumed run %+v, want done with 3 sent", newRun)
	}
	if len(env.sender.sent) != 3 {
		t.Fatalf("sent %v", env.sender.sent)
	}
}

// TestBridgeLaterRunWholeFailureIsFailed covers the same whole-run
// failure in "Send later" mode: a bad base64 global attachment fails
// the run before any row is rendered. The run is failed (not
// cancelled) with the error recorded, and ResumeRun can proceed
// from it — the resumed run fails the same way, deterministically,
// because the stored request still carries the bad attachment.
func TestBridgeLaterRunWholeFailureIsFailed(t *testing.T) {
	env := newBridgeTestEnv(t)
	tmpl := testBridgeTemplate()
	tmpl.GlobalAttachments = []hostmm.GlobalAttachment{
		{Filename: "broken.bin", DataBase64: "!!!not-base64!!!"},
	}
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: tmpl, Rows: testBridgeRows(),
		DeliverMode: "later", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunFailed {
		t.Fatalf("run status %q, want failed", run.Status)
	}
	if run.Error == "" || !strings.Contains(run.Error, "invalid base64") {
		t.Fatalf("run error %q, want the base64 error recorded", run.Error)
	}
	if run.FailedCount != 3 || len(run.Rows) != 3 || run.QueuedCount != 0 {
		t.Fatalf("run %+v, want 3 failed rows, 0 queued", run)
	}
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("queue %+v, want empty", items)
	}
	// Resume proceeds from a failed run (no "no failed rows" error);
	// the resumed run fails identically on the same bad attachment.
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatalf("resume from a failed run must proceed: %v", err)
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunFailed || newRun.FailedCount != 3 {
		t.Fatalf("resumed run %+v, want failed with 3 failed rows", newRun)
	}
}

func TestBridgeWholeRunFailureRowRecordingSurfaced(t *testing.T) {
	env := newBridgeTestEnv(t)
	// The store is lazy-init on the first bridge call; open it
	// before installing the fault-injection trigger.
	if err := env.bridge.ensureInit(); err != nil {
		t.Fatal(err)
	}
	// Fault injection: fail every INSERT into the run-rows table
	// while leaving SELECTs and the runs table alone, so the
	// whole-run failure path runs with row recording broken.
	if _, err := env.bridge.store.DB().Exec(`
		CREATE TRIGGER r23_fail_row_inserts BEFORE INSERT ON mailmerge_run_rows
		BEGIN SELECT RAISE(ABORT, 'r23 test: row recording unavailable'); END;
	`); err != nil {
		t.Fatal(err)
	}
	env.factory.createErr = errors.New("dial tcp: connection refused")
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: testBridgeRows(),
		DeliverMode: "send", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	// The run is still status failed with the connect error — but
	// no failed rows could be recorded.
	if run.Status != hostmm.RunFailed {
		t.Fatalf("run status %q, want failed", run.Status)
	}
	if run.FailedCount != 0 || len(run.Rows) != 0 {
		t.Fatalf("run %+v, want 0 recorded failed rows", run)
	}
	// R23: the row-recording failure is surfaced on the run's error
	// text instead of being swallowed — the empty row log looks
	// like there is nothing to resume, so the run must say so.
	if !strings.Contains(run.Error, "connection refused") || !strings.Contains(run.Error, "could not record 3 failed row(s)") {
		t.Fatalf("run error %q, want the connect error plus the row-recording failure", run.Error)
	}
	// The failed run is still resumable despite zero recorded
	// failed rows: fix the connection, drop the fault, resume —
	// ResumeRun re-runs the whole stored request for a failed run
	// with no failed rows. This is the backend condition the
	// frontend Resume button now reads (status failed, not
	// failedCount > 0).
	env.factory.createErr = nil
	if _, err := env.bridge.store.DB().Exec(`DROP TRIGGER r23_fail_row_inserts`); err != nil {
		t.Fatal(err)
	}
	newID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatalf("resume from a failed run with no recorded failed rows must proceed: %v", err)
	}
	newRun := waitForRun(t, env.bridge, newID)
	if newRun.Status != hostmm.RunDone || newRun.SentCount != 3 || newRun.TotalRows != 3 {
		t.Fatalf("resumed run %+v, want done with 3 sent", newRun)
	}
}

func TestBridgeCancelRun(t *testing.T) {
	env := newBridgeTestEnv(t)
	if err := env.bridge.MailMerge_CancelRun("no-such-run"); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := env.bridge.MailMerge_GetRun("no-such-run"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestBridgeSettings(t *testing.T) {
	env := newBridgeTestEnv(t)
	st, err := env.bridge.MailMerge_GetSettings("acc1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.CapEnabled || st.CapLimit != 100 || st.DefaultDeliverMode != "drafts" || st.DefaultPauseSeconds != 2 {
		t.Fatalf("defaults %+v", st)
	}
	st.CapLimit = 50
	st.DefaultDeliverMode = "send"
	if err := env.bridge.MailMerge_SaveSettings("acc1", st); err != nil {
		t.Fatal(err)
	}
	got, err := env.bridge.MailMerge_GetSettings("acc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.CapLimit != 50 || got.DefaultDeliverMode != "send" {
		t.Fatalf("saved %+v", got)
	}
	// Settings are per-account: another account still sees defaults.
	other, err := env.bridge.MailMerge_GetSettings("acc2")
	if err != nil {
		t.Fatal(err)
	}
	if other.CapLimit != 100 {
		t.Fatalf("other account %+v", other)
	}
	// Invalid settings are rejected.
	if err := env.bridge.MailMerge_SaveSettings("acc1", Settings{CapLimit: 0, DefaultDeliverMode: "drafts"}); err == nil {
		t.Fatal("expected cap-limit error")
	}
	if err := env.bridge.MailMerge_SaveSettings("acc1", Settings{CapLimit: 10, DefaultDeliverMode: "bogus"}); err == nil {
		t.Fatal("expected deliver-mode error")
	}
}

func TestBridgeDisabledGate(t *testing.T) {
	dir := t.TempDir()
	svc := hostmm.NewService(hostmm.ServiceDeps{DraftSaver: &fakeDraftSaver{}})
	b := NewMailMergeBridge(MailMergeBridgeDeps{
		SettingsStore: &fakeSettingsStore{enabled: false},
		Paths:         &platform.Paths{Data: dir},
		Service:       svc,
	})
	if id, err := b.MailMerge_StartRun(hostmm.RunRequest{Template: testBridgeTemplate(), Rows: testBridgeRows(), DeliverMode: "drafts"}); err != nil || id != "" {
		t.Fatalf("disabled start: id %q err %v", id, err)
	}
	if runs, err := b.MailMerge_ListRuns(); err != nil || runs != nil {
		t.Fatalf("disabled list: %v %v", runs, err)
	}
	// Disabled must not create the extension DB.
	if _, err := os.Stat(filepath.Join(dir, "extensions", "mail-merge", "data.db")); !os.IsNotExist(err) {
		t.Fatalf("disabled extension must not open its DB")
	}
}

func TestExtensionRegister(t *testing.T) {
	// The manifest parses and the extension registers its toolbar item
	// + settings tab. Uses a minimal core stub via the real extui
	// registry path is covered by app wiring; here we check the manifest
	// and constructor only.
	ext := NewExtension()
	m := ext.Manifest()
	if m.ID != "mail-merge" || m.Name != "Mail Merge" {
		t.Fatalf("manifest %+v", m)
	}
	found := false
	for _, c := range m.Capabilities {
		if c == "mail.merge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities %v", m.Capabilities)
	}
}

// TestResumeRun_DoesNotResendAlreadySentAddress (fourth-audit finding
// 8): a failed row does not mark its address as seen, so a later row
// to the same address is delivered — and resuming the failed row must
// NOT send that address a second time. The resume seeds its duplicate
// list with the addresses the original run already sent, so the
// failed row is skipped as a duplicate instead of re-sent.
func TestResumeRun_DoesNotResendAlreadySentAddress(t *testing.T) {
	env := newBridgeTestEnv(t)
	env.sender.failOnce = map[string]error{"alice@example.com": errors.New("transient SMTP failure")}
	rows := []map[string]string{
		{"FirstName": "Alice", "LastName": "One", "Email": "alice@example.com"},
		{"FirstName": "Alice", "LastName": "Two", "Email": "alice@example.com"},
	}
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: rows,
		DeliverMode: "send", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if len(run.Rows) != 2 {
		t.Fatalf("run rows %+v", run.Rows)
	}
	if run.Rows[0].Status != hostmm.RowFailed {
		t.Fatalf("row 0 status %q, want failed (transient)", run.Rows[0].Status)
	}
	if run.Rows[1].Status != hostmm.RowSent {
		t.Fatalf("row 1 status %q, want sent", run.Rows[1].Status)
	}
	if len(env.sender.sent) != 1 || env.sender.sent[0] != "alice@example.com" {
		t.Fatalf("sent %v, want exactly one send to alice", env.sender.sent)
	}

	resumedID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := waitForRun(t, env.bridge, resumedID)
	var resumedRow *hostmm.RowResult
	for i := range resumed.Rows {
		if resumed.Rows[i].Index == 0 {
			resumedRow = &resumed.Rows[i]
		}
	}
	if resumedRow == nil {
		t.Fatalf("resumed run rows %+v, want a row for original index 0", resumed.Rows)
	}
	if resumedRow.Status != hostmm.RowSkipped {
		t.Fatalf("resumed row 0 status %q, want skipped (duplicate of already-sent alice)", resumedRow.Status)
	}
	if len(env.sender.sent) != 1 {
		t.Fatalf("sent %v after resume, want still exactly one send — alice was sent twice", env.sender.sent)
	}
}

// TestResumeLaterRun_DeletesOldFailedQueueItem (fourth-audit finding
// 9): resuming a "Send later" run re-renders the failed rows and
// enqueues fresh items carrying the same Message-IDs — the old failed
// queue items must be gone, or the address gets the mail twice (once
// from the resumed copy, once from a later retry of the failed copy).
func TestResumeLaterRun_DeletesOldFailedQueueItem(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	env.sender.fail["jane@example.com"] = errors.New("452 too many emails")

	rows := testBridgeRows()[:2] // john (ok), jane (fails)
	runID := startLaterRun(t, env, rows)
	oldJane := findQueueItem(t, env.bridge, runID, "jane@example.com")
	oldFull, err := env.bridge.store.GetQueueItemForSend(oldJane.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldMessageID := messageIDHeader(oldFull.RawMessage)

	// Drive jane's item to 'failed' (3 attempts, advancing past the
	// backoff each time), exactly like TestQueueRetryFailedAndMaxAttempts.
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := env.bridge.MailMerge_SendUnsent(); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			fakeNow = fakeNow.Add(time.Duration(attempt+1) * time.Minute)
		}
	}
	jane := findQueueItem(t, env.bridge, runID, "jane@example.com")
	if jane.Status != QueueStatusFailed {
		t.Fatalf("jane after 3 attempts %+v, want failed", jane)
	}

	delete(env.sender.fail, "jane@example.com")
	resumedID, err := env.bridge.MailMerge_ResumeRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, env.bridge, resumedID)

	// Exactly one queue item for jane may exist, under the resumed
	// run, carrying the same Message-ID as the failed original.
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	var janes []QueuedItem
	for _, it := range items {
		if it.Recipient == "jane@example.com" {
			janes = append(janes, it)
		}
	}
	if len(janes) != 1 {
		t.Fatalf("queue items for jane: %d %+v, want exactly 1", len(janes), janes)
	}
	if janes[0].RunID != resumedID {
		t.Fatalf("jane item run %q, want resumed run %q", janes[0].RunID, resumedID)
	}
	newFull, err := env.bridge.store.GetQueueItemForSend(janes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDHeader(newFull.RawMessage); got != oldMessageID {
		t.Fatalf("resumed Message-ID %q, want the original %q", got, oldMessageID)
	}

	// Dispatching sends jane exactly once.
	if _, err := env.bridge.MailMerge_SendUnsent(); err != nil {
		t.Fatal(err)
	}
	janeSends := 0
	for _, rcpt := range env.sender.sent {
		if rcpt == "jane@example.com" {
			janeSends++
		}
	}
	if janeSends != 1 {
		t.Fatalf("jane sent %d times, want 1", janeSends)
	}
}

// messageIDHeader extracts the Message-ID header value from rendered
// RFC822 bytes ("" when absent).
func messageIDHeader(raw []byte) string {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "message-id:") {
			return strings.TrimSpace(line[len("Message-ID:"):])
		}
	}
	return ""
}
