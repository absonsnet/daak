package mailmerge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/absonsnet/daak/internal/smtp"
)

// --- fakes ---------------------------------------------------------------

type fakeDraftSaver struct {
	saved []smtp.ComposeMessage
	fail  error // fail every save when non-nil
}

func (f *fakeDraftSaver) SaveDraft(_ string, msg smtp.ComposeMessage) error {
	if f.fail != nil {
		return f.fail
	}
	f.saved = append(f.saved, msg)
	return nil
}

type fakeSender struct {
	sent []sentMail
	// sentRaw records the exact bytes of every transmission that
	// reached the (fake) server, including ones the client then
	// treats as failed — the lost-acknowledgement case.
	sentRaw []string
	fail    map[string]error // fail sends to this address
	// failOnce fails the next send to this address only, then
	// succeeds: a transient mid-send failure for one row.
	failOnce map[string]error
	// failAfterSend, when set for a recipient, records the bytes and
	// then returns the error: the server accepted DATA but the ack
	// was lost, so the worker must retry with identical bytes.
	failAfterSend map[string]error
}

type sentMail struct {
	from string
	to   []string
	raw  []byte
}

func (f *fakeSender) SendMail(from string, to []string, msg []byte) error {
	for _, rcpt := range to {
		if err, ok := f.fail[rcpt]; ok {
			return err
		}
		if err, ok := f.failOnce[rcpt]; ok {
			delete(f.failOnce, rcpt)
			return err
		}
	}
	f.sent = append(f.sent, sentMail{from: from, to: to, raw: msg})
	f.sentRaw = append(f.sentRaw, string(msg))
	for _, rcpt := range to {
		if err, ok := f.failAfterSend[rcpt]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeSender) Close() error { return nil }

type fakeSenderFactory struct {
	sender    *fakeSender
	created   int
	createErr error
}

func (f *fakeSenderFactory) NewSender(_ context.Context, _ string) (MailSender, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created++
	return f.sender, nil
}

type fakeSentSaver struct {
	saved [][]byte
	fail  error
}

func (f *fakeSentSaver) SaveToSent(_ context.Context, _ string, raw []byte) error {
	if f.fail != nil {
		return f.fail
	}
	f.saved = append(f.saved, raw)
	return nil
}

type fakeContactLister struct {
	rows []map[string]string
	cols []string
	err  error
}

func (f *fakeContactLister) ListContacts() ([]map[string]string, []string, error) {
	return f.rows, f.cols, f.err
}

// --- helpers -------------------------------------------------------------

func testTemplate() Template {
	return Template{
		AccountID: "acc1",
		From:      "Sender <sender@example.com>",
		To:        "{{FirstName}} {{LastName}} <{{Email}}>",
		Subject:   "Hello {{FirstName}}",
		BodyHTML:  "<p>Hi {{FirstName}}, your code is {{Code}}.</p>",
		BodyText:  "Hi {{FirstName}}, your code is {{Code}}.",
	}
}

func testRows() []map[string]string {
	return []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2"},
		{"FirstName": "Bob", "LastName": "Lee", "Email": "bob@example.com", "Code": "C3"},
	}
}

// --- end-to-end render ---------------------------------------------------

func TestServiceDraftsEndToEndThreeRows(t *testing.T) {
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 3 || out.FailedCount != 0 || out.SkippedCount != 0 || out.HeldCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(saver.saved) != 3 {
		t.Fatalf("saved %d drafts", len(saver.saved))
	}
	want := []struct{ to, subject, body string }{
		{"John Doe <john@example.com>", "Hello John", "Hi John, your code is A1."},
		{"Jane Smith <jane@example.com>", "Hello Jane", "Hi Jane, your code is B2."},
		{"Bob Lee <bob@example.com>", "Hello Bob", "Hi Bob, your code is C3."},
	}
	for i, w := range want {
		m := saver.saved[i]
		if len(m.To) != 1 || m.To[0].Name != strings.SplitN(w.to, " <", 2)[0] || m.To[0].Address != testRows()[i]["Email"] {
			t.Fatalf("row %d To %v want %q", i, m.To, w.to)
		}
		if m.Subject != w.subject {
			t.Fatalf("row %d subject %q want %q", i, m.Subject, w.subject)
		}
		if !strings.Contains(m.TextBody, w.body) {
			t.Fatalf("row %d text body %q want %q", i, m.TextBody, w.body)
		}
		if !strings.Contains(m.HTMLBody, w.body) {
			t.Fatalf("row %d html body %q want %q", i, m.HTMLBody, w.body)
		}
		if m.From.Address != "sender@example.com" {
			t.Fatalf("row %d from %q", i, m.From.Address)
		}
	}
}

func TestServiceSendReusesOneConnection(t *testing.T) {
	sender := &fakeSender{}
	factory := &fakeSenderFactory{sender: sender}
	sentSaver := &fakeSentSaver{}
	svc := NewService(ServiceDeps{SenderFactory: factory, SentSaver: sentSaver})
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if factory.created != 1 {
		t.Fatalf("sender created %d times, want 1", factory.created)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent %d", len(sender.sent))
	}
	if len(sentSaver.saved) != 3 {
		t.Fatalf("saved to Sent %d, want 3", len(sentSaver.saved))
	}
	if out.SentCount != 3 || len(results) != 3 {
		t.Fatalf("outcome %+v results %d", out, len(results))
	}
	for i, r := range results {
		if r.Status != RowSent || r.Index != i {
			t.Fatalf("result %d: %+v", i, r)
		}
	}
	// A Sent-save failure must not fail the send.
	sentSaver.fail = errors.New("imap down")
	sender2 := &fakeSender{}
	svc2 := NewService(ServiceDeps{SenderFactory: &fakeSenderFactory{sender: sender2}, SentSaver: sentSaver})
	out, err = svc2.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows()[:1],
		DeliverMode: "send", AccountID: "acc1",
	}, nil)
	if err != nil || out.SentCount != 1 || len(sender2.sent) != 1 {
		t.Fatalf("sent-save failure must not fail send: out %+v err %v", out, err)
	}
}

func TestServiceCapSendOnly(t *testing.T) {
	sender := &fakeSender{}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
	})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "send", AccountID: "acc1",
		CapEnabled: true, CapLimit: 2,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 2 || out.HeldCount != 1 {
		t.Fatalf("cap outcome %+v", out)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d", len(sender.sent))
	}
}

func TestServiceCapDoesNotApplyToDrafts(t *testing.T) {
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "drafts", AccountID: "acc1",
		CapEnabled: true, CapLimit: 1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 3 || out.HeldCount != 0 || len(saver.saved) != 3 {
		t.Fatalf("drafts must ignore cap: %+v saved %d", out, len(saver.saved))
	}
}

func TestServiceSkipAndContinue(t *testing.T) {
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1"},
		{"FirstName": "Bad", "LastName": "Row", "Email": "", "Code": "B2"},               // empty To → skip
		{"FirstName": "Bad", "LastName": "Email", "Email": "not-an-email", "Code": "C3"}, // invalid → skip
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "D4"},
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "E5"}, // duplicate → skip
		{"FirstName": "Bob", "LastName": "Lee", "Email": "bob@example.com", "Code": "F6"},
	}
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows,
		DeliverMode: "drafts", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 3 || out.SkippedCount != 3 || out.FailedCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(results) != 6 {
		t.Fatalf("results %d", len(results))
	}
	if results[1].Status != RowSkipped || results[2].Status != RowSkipped || results[4].Status != RowSkipped {
		t.Fatalf("skip statuses: %+v", results)
	}
	if !strings.Contains(results[4].Error, "duplicate") {
		t.Fatalf("duplicate error %q", results[4].Error)
	}
	// A To that still contains {{ after rendering is never sent.
	rows2 := []map[string]string{{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com"}}
	tmpl := testTemplate()
	tmpl.To = "{{Missing}}"
	out, err = svc.Execute(context.Background(), RunRequest{
		Template: tmpl, Rows: rows2, DeliverMode: "drafts", AccountID: "acc1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.FailedCount != 1 || len(saver.saved) != 3 {
		t.Fatalf("unresolved To must fail the row: %+v saved %d", out, len(saver.saved))
	}
}

func TestServicePerRowSendFailureContinues(t *testing.T) {
	sender := &fakeSender{fail: map[string]error{"jane@example.com": errors.New("452 too many emails")}}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
	})
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 2 || out.FailedCount != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if results[1].Status != RowFailed || !strings.Contains(results[1].Error, "452") {
		t.Fatalf("failed row: %+v", results[1])
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d", len(sender.sent))
	}
}

// TestServiceFailedRowDoesNotPoisonDuplicateSuppression is the
// finding-14 regression test: a row that fails mid-send must not
// mark its address seen, so a later row to the same address is
// delivered (not skipped as a false "duplicate"), and the failed
// row stays failed in the log — resumable via ResumeRun, which
// re-runs RowFailed rows only (bridge_test.go covers the resume).
func TestServiceFailedRowDoesNotPoisonDuplicateSuppression(t *testing.T) {
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1"},
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A2"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2"},
	}
	sender := &fakeSender{failOnce: map[string]error{"john@example.com": errors.New("452 try again later")}}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
	})
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows,
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results %d", len(results))
	}
	if results[0].Status != RowFailed || !strings.Contains(results[0].Error, "452") {
		t.Fatalf("row 0 must fail: %+v", results[0])
	}
	if results[1].Status != RowSent {
		t.Fatalf("row 1 (same address, after a failed row) must send, got %+v", results[1])
	}
	if results[2].Status != RowSent {
		t.Fatalf("row 2 must send: %+v", results[2])
	}
	if out.SentCount != 2 || out.FailedCount != 1 || out.SkippedCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d, want 2", len(sender.sent))
	}
	// The same rule in drafts mode: a failed draft save must not
	// block a later row to the same address.
	saver := &fakeDraftSaver{}
	// Fail the first save only, via a counting saver.
	counting := &failOnceDraftSaver{inner: saver, fail: errors.New("disk full")}
	svc2 := NewService(ServiceDeps{DraftSaver: counting})
	results = nil
	out, err = svc2.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows,
		DeliverMode: "drafts", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Status != RowFailed || results[1].Status != RowDraftSaved || results[2].Status != RowDraftSaved {
		t.Fatalf("drafts results %+v", results)
	}
	if out.SentCount != 2 || out.FailedCount != 1 {
		t.Fatalf("drafts outcome %+v", out)
	}
	if len(saver.saved) != 2 {
		t.Fatalf("drafts saved %d, want 2", len(saver.saved))
	}
}

// messageIDOf extracts the Message-ID header value from raw RFC822
// bytes ("" when absent).
func messageIDOf(raw string) string {
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "Message-ID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Message-ID:"))
		}
	}
	return ""
}

// TestServiceSendRetryReusesMessageID is the re-audit R3 regression
// test: a direct-send row whose SMTP acknowledgement is lost (the
// server accepted DATA, the client sees an error) is recorded failed
// with the Message-ID assigned at render time — and a retry that
// carries that ID rebuilds the same mail with the same Message-ID,
// so the server can recognise the duplicate instead of delivering
// two copies.
func TestServiceSendRetryReusesMessageID(t *testing.T) {
	sender := &fakeSender{failAfterSend: map[string]error{"john@example.com": errors.New("read tcp: i/o timeout")}}
	svc := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender},
		SentSaver:     &fakeSentSaver{},
	})
	rows := testRows()[:1]
	var results []RowResult
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows,
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.FailedCount != 1 || len(results) != 1 || results[0].Status != RowFailed {
		t.Fatalf("first attempt must fail the row: %+v %+v", out, results)
	}
	firstID := results[0].MessageID
	if firstID == "" {
		t.Fatal("failed row must carry the Message-ID assigned at render time")
	}
	if len(sender.sentRaw) != 1 || messageIDOf(sender.sentRaw[0]) != firstID {
		t.Fatalf("first transmission must carry %q: %q", firstID, sender.sentRaw)
	}

	// Retry with the ID the failed row recorded: the rebuilt mail
	// must carry the same Message-ID.
	delete(sender.failAfterSend, "john@example.com")
	results = nil
	out, err = svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows, MessageIDs: []string{firstID},
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 1 || len(results) != 1 || results[0].Status != RowSent {
		t.Fatalf("retry must send the row: %+v %+v", out, results)
	}
	if results[0].MessageID != firstID {
		t.Fatalf("retry row Message-ID %q, want %q", results[0].MessageID, firstID)
	}
	if len(sender.sentRaw) != 2 || messageIDOf(sender.sentRaw[1]) != firstID {
		t.Fatalf("retry transmission must carry %q: %q", firstID, sender.sentRaw)
	}

	// Control: a fresh run with no carried ID mints a new one — the
	// reuse above comes from the carried ID, not determinism.
	results = nil
	out, err = svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows,
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.sentRaw) != 3 || messageIDOf(sender.sentRaw[2]) == firstID {
		t.Fatalf("fresh render must mint a new Message-ID: %q", sender.sentRaw)
	}

	// And two rows in one run get distinct IDs.
	sender2 := &fakeSender{}
	svc2 := NewService(ServiceDeps{
		SenderFactory: &fakeSenderFactory{sender: sender2},
		SentSaver:     &fakeSentSaver{},
	})
	results = nil
	if _, err := svc2.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows()[:2],
		DeliverMode: "send", AccountID: "acc1",
	}, func(r RowResult) { results = append(results, r) }); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].MessageID == "" || results[0].MessageID == results[1].MessageID {
		t.Fatalf("rows must get distinct Message-IDs: %+v", results)
	}
}

// failOnceDraftSaver fails the first SaveDraft call, then delegates.
type failOnceDraftSaver struct {
	inner *fakeDraftSaver
	fail  error
	done  bool
}

func (f *failOnceDraftSaver) SaveDraft(accountID string, msg smtp.ComposeMessage) error {
	if !f.done {
		f.done = true
		return f.fail
	}
	return f.inner.SaveDraft(accountID, msg)
}

func TestServiceStartStop(t *testing.T) {
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "drafts", AccountID: "acc1",
		StartRow: 2, StopRow: 3,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 2 || len(saver.saved) != 2 {
		t.Fatalf("outcome %+v saved %d", out, len(saver.saved))
	}
	if saver.saved[0].To[0].Address != "jane@example.com" || saver.saved[1].To[0].Address != "bob@example.com" {
		t.Fatalf("wrong slice: %v", saver.saved)
	}
}

func TestServiceCancel(t *testing.T) {
	// Cancel before the run starts: every row is recorded skipped.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	out, err := svc.Execute(ctx, RunRequest{
		Template: testTemplate(), Rows: testRows(),
		DeliverMode: "drafts", AccountID: "acc1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Cancelled || out.SkippedCount != 3 || len(saver.saved) != 0 {
		t.Fatalf("cancel outcome %+v saved %d", out, len(saver.saved))
	}
}

func TestServicePerRowAttachment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("PDF-A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.pdf"), []byte("PDF-B"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate()
	tmpl.AttachmentField = "{{Attachment}}"
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1", "Attachment": "a.pdf"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2", "Attachment": "missing.pdf"},
		{"FirstName": "Bob", "LastName": "Lee", "Email": "bob@example.com", "Code": "C3", "Attachment": "b.pdf"},
	}
	saver := &fakeDraftSaver{}
	svc := NewService(ServiceDeps{DraftSaver: saver})
	out, err := svc.Execute(context.Background(), RunRequest{
		Template: tmpl, Rows: rows, DeliverMode: "drafts",
		AccountID: "acc1", AttachmentDir: dir,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.SentCount != 2 || out.SkippedCount != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if len(saver.saved[0].Attachments) != 1 || string(saver.saved[0].Attachments[0].Content) != "PDF-A" {
		t.Fatalf("row 0 attachments %v", saver.saved[0].Attachments)
	}
	if string(saver.saved[1].Attachments[0].Content) != "PDF-B" {
		t.Fatalf("row 2 (saved[1]) attachment %q", saver.saved[1].Attachments[0].Content)
	}
}

func TestServiceValidationErrors(t *testing.T) {
	svc := NewService(ServiceDeps{})
	if _, err := svc.Execute(context.Background(), RunRequest{DeliverMode: "drafts"}, nil); err == nil {
		t.Fatal("expected account-required error")
	}
	if _, err := svc.Execute(context.Background(), RunRequest{DeliverMode: "bogus", AccountID: "a"}, nil); err == nil {
		t.Fatal("expected bad-mode error")
	}
	if _, err := svc.Execute(context.Background(), RunRequest{DeliverMode: "drafts", AccountID: "a"}, nil); err == nil {
		t.Fatal("expected missing-draft-saver error")
	}
}

func TestServiceListContacts(t *testing.T) {
	rows := []map[string]string{{"PrimaryEmail": "a@example.com"}}
	svc := NewService(ServiceDeps{ContactLister: &fakeContactLister{rows: rows, cols: []string{"PrimaryEmail"}}})
	got, cols, err := svc.ListContacts()
	if err != nil || len(got) != 1 || len(cols) != 1 {
		t.Fatalf("got %v %v err %v", got, cols, err)
	}
	svc2 := NewService(ServiceDeps{})
	if _, _, err := svc2.ListContacts(); err == nil {
		t.Fatal("expected no-lister error")
	}
}

func TestValidate(t *testing.T) {
	tmpl := testTemplate()
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2"},
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "C3"}, // duplicate
		{"FirstName": "No", "LastName": "Email", "Email": "", "Code": "D4"},                 // empty To
		{"FirstName": "Bad", "LastName": "Email", "Email": "not-an-email", "Code": "E5"},    // invalid
	}
	res := Validate(tmpl, rows, "")
	if len(res.UnresolvedVariables) != 0 {
		t.Fatalf("unresolved %v", res.UnresolvedVariables)
	}
	if res.DuplicateEmails != 1 {
		t.Fatalf("duplicates %d", res.DuplicateEmails)
	}
	// Row 3 renders "No Email <>" — not empty, but an invalid address.
	if len(res.EmptyToRows) != 0 {
		t.Fatalf("emptyTo %v", res.EmptyToRows)
	}
	if len(res.InvalidEmails) != 2 || res.InvalidEmails[0].Row != 3 || res.InvalidEmails[1].Row != 4 {
		t.Fatalf("invalid %v", res.InvalidEmails)
	}
	if len(res.MissingAttachmentFiles) != 0 {
		t.Fatalf("attachments %v", res.MissingAttachmentFiles)
	}
	// A template whose To renders to "" reports the row as empty-To.
	emptyTmpl := tmpl
	emptyTmpl.To = "{{Email}}"
	res = Validate(emptyTmpl, rows, "")
	if len(res.EmptyToRows) != 1 || res.EmptyToRows[0] != 3 {
		t.Fatalf("emptyTo (bare-email template) %v", res.EmptyToRows)
	}
}

func TestValidateUnresolvedBlocks(t *testing.T) {
	tmpl := testTemplate()
	tmpl.Subject = "Hi {{MissingCol}}"
	res := Validate(tmpl, testRows(), "")
	if len(res.UnresolvedVariables) != 1 || res.UnresolvedVariables[0] != "MissingCol" {
		t.Fatalf("unresolved %v", res.UnresolvedVariables)
	}
}

func TestValidateMissingAttachment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exists.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl := testTemplate()
	tmpl.AttachmentField = "{{Attachment}}"
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1", "Attachment": "exists.pdf"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2", "Attachment": "gone.pdf"},
	}
	res := Validate(tmpl, rows, dir)
	if len(res.MissingAttachmentFiles) != 1 || res.MissingAttachmentFiles[0].Row != 1 {
		t.Fatalf("missing %v", res.MissingAttachmentFiles)
	}
	if !strings.Contains(res.MissingAttachmentFiles[0].Path, "gone.pdf") {
		t.Fatalf("path %q", res.MissingAttachmentFiles[0].Path)
	}
}

func TestRenderRowPreview(t *testing.T) {
	tmpl := testTemplate()
	tmpl.AttachmentField = "{{Attachment}}"
	row := map[string]string{"FirstName": "John <b>", "LastName": "Doe", "Email": "john@example.com", "Code": "A1", "Attachment": "a.pdf"}
	msg, err := RenderRow(tmpl, row, "/tmp", 5)
	if err != nil {
		t.Fatal(err)
	}
	if msg.RowIndex != 5 || msg.To != "John <b> Doe <john@example.com>" {
		t.Fatalf("msg %+v", msg)
	}
	// HTML body escapes the value; text body does not.
	if !strings.Contains(msg.BodyHTML, "John &lt;b&gt;") {
		t.Fatalf("html body %q", msg.BodyHTML)
	}
	if !strings.Contains(msg.BodyText, "John <b>") {
		t.Fatalf("text body %q", msg.BodyText)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0] != filepath.Join("/tmp", "a.pdf") {
		t.Fatalf("attachments %v", msg.Attachments)
	}
}

func TestSliceRows(t *testing.T) {
	rows := testRows()
	sel, idx := SliceRows(rows, 0, 0)
	if len(sel) != 3 || idx[2] != 2 {
		t.Fatalf("full slice %d %v", len(sel), idx)
	}
	sel, idx = SliceRows(rows, 2, 2)
	if len(sel) != 1 || idx[0] != 1 || sel[0]["Email"] != "jane@example.com" {
		t.Fatalf("single slice %v %v", sel, idx)
	}
	sel, idx = SliceRows(rows, 9, 0)
	if len(sel) != 0 || len(idx) != 0 {
		t.Fatalf("out-of-range slice %v", sel)
	}
}

// --- RenderForQueue / RawSender (Phase 2) --------------------------------

func TestRenderForQueue(t *testing.T) {
	svc := NewService(ServiceDeps{})
	var queued []QueuedMessage
	var results []RowResult
	out, err := svc.RenderForQueue(context.Background(), RunRequest{
		Template: testTemplate(), Rows: testRows(), AccountID: "acc1",
	}, func(qm QueuedMessage) error { queued = append(queued, qm); return nil },
		func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.QueuedCount != 3 || out.SentCount != 0 || out.FailedCount != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(queued) != 3 || len(results) != 3 {
		t.Fatalf("queued %d results %d", len(queued), len(results))
	}
	for i, qm := range queued {
		row := testRows()[i]
		if qm.RowIndex != i || qm.Email != row["Email"] {
			t.Fatalf("row %d queued %+v", i, qm)
		}
		if qm.From != "sender@example.com" || len(qm.To) != 1 || qm.To[0] != row["Email"] {
			t.Fatalf("row %d envelope from %q to %v", i, qm.From, qm.To)
		}
		if qm.Subject != "Hello "+row["FirstName"] {
			t.Fatalf("row %d subject %q", i, qm.Subject)
		}
		// The raw bytes are the final rendered message.
		raw := string(qm.Raw)
		if !strings.Contains(raw, "Hello "+row["FirstName"]) || !strings.Contains(raw, row["Email"]) {
			t.Fatalf("row %d raw %q", i, raw)
		}
		if !strings.Contains(raw, "Hi "+row["FirstName"]+", your code is "+row["Code"]+".") {
			t.Fatalf("row %d raw missing body: %q", i, raw)
		}
		if results[i].Status != RowQueued || results[i].Index != i || results[i].Email != row["Email"] {
			t.Fatalf("row %d result %+v", i, results[i])
		}
	}
	// Duplicates are skipped, not queued — same rule as Execute.
	dupRows := append(testRows(), testRows()[0])
	queued = nil
	results = nil
	out, err = svc.RenderForQueue(context.Background(), RunRequest{
		Template: testTemplate(), Rows: dupRows, AccountID: "acc1",
	}, func(qm QueuedMessage) error { queued = append(queued, qm); return nil },
		func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if out.QueuedCount != 3 || out.SkippedCount != 1 || len(queued) != 3 {
		t.Fatalf("dup outcome %+v queued %d", out, len(queued))
	}
	// An invalid row is skipped (empty To) and the run continues.
	badRows := []map[string]string{
		{"FirstName": "No", "LastName": "Mail", "Email": "", "Code": "X"},
		testRows()[0],
	}
	queued = nil
	out, err = svc.RenderForQueue(context.Background(), RunRequest{
		Template: testTemplate(), Rows: badRows, AccountID: "acc1",
	}, func(qm QueuedMessage) error { queued = append(queued, qm); return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.QueuedCount != 1 || out.SkippedCount != 1 || len(queued) != 1 {
		t.Fatalf("bad-row outcome %+v queued %d", out, len(queued))
	}
}

// TestRenderForQueueEnqueueFailureFailsRow is the finding-14
// queue-mode regression test (and the service half of finding 17):
// when persisting a rendered row fails, the row is RowFailed — not
// RowQueued — and its address is not marked seen, so a later row to
// the same address still queues.
func TestRenderForQueueEnqueueFailureFailsRow(t *testing.T) {
	svc := NewService(ServiceDeps{})
	rows := []map[string]string{
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A1"},
		{"FirstName": "John", "LastName": "Doe", "Email": "john@example.com", "Code": "A2"},
		{"FirstName": "Jane", "LastName": "Smith", "Email": "jane@example.com", "Code": "B2"},
	}
	var queued []QueuedMessage
	var results []RowResult
	enqueueErr := errors.New("queue store unavailable")
	failedOnce := false
	onQueued := func(qm QueuedMessage) error {
		if qm.Email == "john@example.com" && !failedOnce {
			failedOnce = true
			return enqueueErr // first john fails to persist
		}
		queued = append(queued, qm)
		return nil
	}
	out, err := svc.RenderForQueue(context.Background(), RunRequest{
		Template: testTemplate(), Rows: rows, AccountID: "acc1",
	}, onQueued, func(r RowResult) { results = append(results, r) })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("results %d", len(results))
	}
	if results[0].Status != RowFailed || !strings.Contains(results[0].Error, "enqueue") {
		t.Fatalf("row 0 must fail with an enqueue error: %+v", results[0])
	}
	if results[1].Status != RowQueued {
		t.Fatalf("row 1 (same address, after a failed enqueue) must queue, got %+v", results[1])
	}
	if results[2].Status != RowQueued {
		t.Fatalf("row 2 must queue: %+v", results[2])
	}
	if out.QueuedCount != 2 || out.FailedCount != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if len(queued) != 2 || queued[0].Email != "john@example.com" || queued[1].Email != "jane@example.com" {
		t.Fatalf("queued %+v", queued)
	}
}

func TestRawSenderSendsAndSavesToSent(t *testing.T) {
	sender := &fakeSender{}
	saved := &fakeSentSaver{}
	factory := &fakeSenderFactory{sender: sender}
	svc := NewService(ServiceDeps{SenderFactory: factory, SentSaver: saved})
	rs, err := svc.OpenRawSender(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	msg := QueuedMessage{
		Email: "john@example.com", From: "sender@example.com",
		To: []string{"john@example.com"}, Raw: []byte("Subject: hi\r\n\r\nbody"),
	}
	// The hook runs the moment SMTP accepts, before the Sent save:
	// at hook time nothing has been saved to Sent yet.
	var hookSavedCount = -1
	hook := func() { hookSavedCount = len(saved.saved) }
	if err := rs.Send(context.Background(), msg, hook); err != nil {
		t.Fatal(err)
	}
	if hookSavedCount != 0 {
		t.Fatalf("afterSMTP hook ran after %d Sent saves, want 0 (hook must precede the Sent save)", hookSavedCount)
	}
	if err := rs.Send(context.Background(), msg, hook); err != nil {
		t.Fatal(err)
	}
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	// One connection for both sends.
	if factory.created != 1 {
		t.Fatalf("connections created %d, want 1", factory.created)
	}
	if len(sender.sent) != 2 || sender.sent[0].from != "sender@example.com" {
		t.Fatalf("sent %+v", sender.sent)
	}
	if len(saved.saved) != 2 || string(saved.saved[0]) != "Subject: hi\r\n\r\nbody" {
		t.Fatalf("saved to Sent %d", len(saved.saved))
	}
}

func TestRawSenderSendFailureDoesNotSaveToSent(t *testing.T) {
	sender := &fakeSender{fail: map[string]error{"jane@example.com": errors.New("452 too many emails")}}
	saved := &fakeSentSaver{}
	svc := NewService(ServiceDeps{SenderFactory: &fakeSenderFactory{sender: sender}, SentSaver: saved})
	rs, err := svc.OpenRawSender(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	msg := QueuedMessage{
		Email: "jane@example.com", From: "sender@example.com",
		To: []string{"jane@example.com"}, Raw: []byte("Subject: hi\r\n\r\nbody"),
	}
	hookCalled := false
	if err := rs.Send(context.Background(), msg, func() { hookCalled = true }); err == nil {
		t.Fatal("expected send error")
	}
	if hookCalled {
		t.Fatal("afterSMTP hook must not run when SMTP rejects the mail")
	}
	if len(saved.saved) != 0 {
		t.Fatalf("failed send must not save to Sent: %d", len(saved.saved))
	}
	// A Sent-save failure must NOT fail the send (message is out).
	sender.fail = map[string]error{}
	saved.fail = errors.New("imap append failed")
	if err := rs.Send(context.Background(), QueuedMessage{
		Email: "john@example.com", From: "sender@example.com",
		To: []string{"john@example.com"}, Raw: []byte("x"),
	}, nil); err != nil {
		t.Fatalf("Sent-save failure must not fail the send: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d", len(sender.sent))
	}
}

func TestOpenRawSenderWithoutFactory(t *testing.T) {
	svc := NewService(ServiceDeps{})
	if _, err := svc.OpenRawSender(context.Background(), "acc1"); err == nil {
		t.Fatal("expected error without a sender factory")
	}
}
