package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
}

func (f *fakeSenderFactory) NewSender(_ context.Context, _ string) (hostmm.MailSender, error) {
	f.created++
	return f.sender, nil
}

type fakeSentSaver struct{ saved int }

func (f *fakeSentSaver) SaveToSent(_ context.Context, _ string, _ []byte) error {
	f.saved++
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
		case hostmm.RunDone, hostmm.RunCancelled, hostmm.RunHeld:
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
		CapEnabled: true, CapLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunHeld || run.SentCount != 1 || run.HeldCount != 2 {
		t.Fatalf("cap run %+v", run)
	}
	if len(env.sender.sent) != 1 {
		t.Fatalf("sent %v", env.sender.sent)
	}
	// Next batch: one more, one still held.
	if err := env.bridge.MailMerge_SendNextBatch(runID); err != nil {
		t.Fatal(err)
	}
	run = waitForRun(t, env.bridge, runID)
	if run.Status != hostmm.RunHeld || run.SentCount != 2 || run.HeldCount != 1 {
		t.Fatalf("after next batch %+v", run)
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
