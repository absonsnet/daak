package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/account"
	"github.com/absonsnet/daak/internal/contact"
	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/draft"
	"github.com/absonsnet/daak/internal/folder"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/pgp"
	"github.com/absonsnet/daak/internal/sendqueue"
	"github.com/absonsnet/daak/internal/settings"
	"github.com/absonsnet/daak/internal/smime"
	"github.com/absonsnet/daak/internal/smtp"
)

// sendQueueTestHarness drives the real sendQueueService against a
// temp DB: QueueSend validates and enqueues, dispatchDue re-runs
// composeOps.sendMessage with rawSendOverride capturing the SMTP
// bytes instead of reaching a server. No GUI, no network.
//
// Drafts are real rows (saveDraft writes through draftOps), so the
// undo-window draft behaviour — duplicate-queue rejection, snapshot
// restore on Undo, keeping an edited draft after dispatch — is
// exercised against the same code the app uses. There are no folders
// in the test DB, so draft cleanup after a send only touches the
// local draft row (no IMAP leg).
type sendQueueTestHarness struct {
	svc        *sendQueueService
	db         *database.DB
	draftStore *draft.Store
	sent       [][]byte
	sendErr    error
	now        time.Time
}

func newSendQueueTestHarness(t *testing.T) *sendQueueTestHarness {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT INTO accounts (id, name, email, imap_host, smtp_host, username) VALUES ('acc-1', 'Me', 'me@example.com', 'imap.example.com', 'smtp.example.com', 'me@example.com')`); err != nil {
		t.Fatal(err)
	}

	h := &sendQueueTestHarness{now: time.Now().UTC().Truncate(time.Second)}
	log := logging.WithComponent("test")
	accountStore := account.NewStore(db)
	draftStore := draft.NewStore(db)
	dops := &draftOps{
		accountStore: accountStore,
		folderStore:  folder.NewStore(db),
		draftStore:   draftStore,
	}
	ops := &composeOps{
		accountStore: accountStore,
		contactStore: contact.NewStore(db.DB),
		smimeStore:   smime.NewStore(db.DB, log),
		pgpStore:     pgp.NewStore(db.DB, log),
		draftOps:     dops,
		rawSendOverride: func(_ context.Context, _ string, _ []string, raw []byte) error {
			if h.sendErr != nil {
				return h.sendErr
			}
			h.sent = append(h.sent, raw)
			return nil
		},
		sentSaveOverride: func(_ context.Context, _ string, _ []byte) error { return nil },
	}
	h.db = db
	h.draftStore = draftStore
	h.svc = &sendQueueService{
		store:      sendqueue.NewStore(db),
		settings:   settings.NewStore(db),
		ops:        ops,
		draftStore: draftStore,
		ctx:        context.Background(),
		now:        func() time.Time { return h.now },
		saveDraft: func(accountID string, msg smtp.ComposeMessage, draftID string) (string, error) {
			var existing *draft.Draft
			if draftID != "" {
				existing, _ = draftStore.Get(draftID)
			}
			enc, err := dops.encryptDraftBody(accountID, msg.From.Address, msg)
			if err != nil {
				return "", err
			}
			saved, err := dops.saveDraftToDB(accountID, existing, msg, enc)
			if err != nil {
				return "", err
			}
			return saved.ID, nil
		},
	}
	return h
}

func testComposeMessage() smtp.ComposeMessage {
	return smtp.ComposeMessage{
		From:     smtp.Address{Name: "Me", Address: "me@example.com"},
		To:       []smtp.Address{{Name: "Ada", Address: "ada@example.com"}},
		Subject:  "hello",
		TextBody: "hi there",
	}
}

func (h *sendQueueTestHarness) queue(t *testing.T) QueueSendResult {
	t.Helper()
	res, err := h.svc.QueueSend("acc-1", testComposeMessage(), "")
	if err != nil {
		t.Fatalf("QueueSend: %v", err)
	}
	return res
}

func (h *sendQueueTestHarness) mustItem(t *testing.T, id string) *sendqueue.Item {
	t.Helper()
	item, err := h.svc.store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if item == nil {
		t.Fatalf("queue item %q not found", id)
	}
	return item
}

func TestQueueSendThenDispatch(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	if res.DelaySeconds != settings.DefaultUndoSendDelay {
		t.Fatalf("DelaySeconds = %d, want %d", res.DelaySeconds, settings.DefaultUndoSendDelay)
	}
	if res.DraftID == "" {
		t.Fatalf("DraftID is empty, want a saved draft id")
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusPending {
		t.Fatalf("status = %q, want pending", item.Status)
	}
	if item.Subject != "hello" || item.Recipient != "ada@example.com" {
		t.Fatalf("item = %+v, want subject hello / recipient ada@example.com", item)
	}

	// Inside the undo window: nothing sends.
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(in window) = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages inside the undo window, want 0", len(h.sent))
	}

	// Past the window: the worker sends it, once.
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after window) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 || !strings.Contains(string(h.sent[0]), "hello") {
		t.Fatalf("sent = %d messages, want 1 containing the subject", len(h.sent))
	}
	// Sent rows are deleted, not kept: the Outbox holds no history.
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after dispatch = %+v, %v, want nil, nil (row deleted)", got, err)
	}

	// A second tick sends nothing more (no double-send).
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("second dispatchDue = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages after second tick, want 1", len(h.sent))
	}
}

func TestUndoSendCancelsAndDispatchSkips(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	undone, err := h.svc.UndoSend(res.QueueID)
	if err != nil {
		t.Fatalf("UndoSend: %v", err)
	}
	if undone.DraftID != res.DraftID || undone.AccountID != "acc-1" {
		t.Fatalf("UndoSend = %+v, want draft %q / acc-1", undone, res.DraftID)
	}
	// Undone rows are deleted, not kept as cancelled history.
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after undo = %+v, %v, want nil, nil (row deleted)", got, err)
	}

	// Past the window: the cancelled item never sends.
	h.now = h.now.Add(time.Minute)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(after undo) = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages after undo, want 0", len(h.sent))
	}

	// Undoing twice fails: the message is already back in Drafts.
	if _, err := h.svc.UndoSend(res.QueueID); err == nil {
		t.Fatalf("second UndoSend succeeded, want an error")
	}
}

func TestUndoSendAfterDispatchFails(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.now = h.now.Add(6 * time.Second)
	if _, err := h.svc.dispatchDue(h.now); err != nil {
		t.Fatal(err)
	}
	// The row is deleted on success, so there is nothing left to undo.
	if _, err := h.svc.UndoSend(res.QueueID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("UndoSend after dispatch = %v, want a 'not found' error", err)
	}
}

func TestTransientFailureRetriesWithBackoff(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.sendErr = fmt.Errorf("failed to connect to SMTP server: connection refused")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(failing) = %d, %v, want 0, nil", n, err)
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusPending || item.Attempts != 1 {
		t.Fatalf("after transient failure: %+v, want pending / 1 attempt", item)
	}
	if !item.DueAt.Equal(h.now.Add(time.Minute)) {
		t.Fatalf("dueAt = %v, want now + 1m backoff", item.DueAt)
	}

	// Before the backoff elapses: still nothing.
	h.now = h.now.Add(30 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(in backoff) = %d, %v, want 0, nil", n, err)
	}

	// After the backoff, with the server back: sends.
	h.now = h.now.Add(31 * time.Second)
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after backoff) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
}

func TestPermanentFailureFailsAtOnce(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.sendErr = fmt.Errorf("failed to login to SMTP server: 535 Authentication failed")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue = %d, %v, want 0, nil", n, err)
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusFailed || item.Attempts != 1 {
		t.Fatalf("after permanent failure: %+v, want failed / 1 attempt", item)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages, want 0", len(h.sent))
	}

	// Retry returns it to the queue, due immediately, fresh budget.
	if err := h.svc.RetrySendQueueItem(res.QueueID); err != nil {
		t.Fatalf("RetrySendQueueItem: %v", err)
	}
	item = h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusPending || item.Attempts != 0 {
		t.Fatalf("after retry: %+v, want pending / 0 attempts", item)
	}
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after retry) = %d, %v, want 1, nil", n, err)
	}
}

func TestTransientFailuresExhaustAttempts(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.sendErr = fmt.Errorf("failed to send message: read tcp: i/o timeout")

	// Fail until the budget is exhausted: 1m, 2m, 4m, 8m, 16m.
	for attempt := 1; attempt <= sendqueue.DefaultMaxAttempts; attempt++ {
		h.now = h.now.Add(6*time.Second + sendqueue.Backoff(attempt-1))
		if _, err := h.svc.dispatchDue(h.now); err != nil {
			t.Fatal(err)
		}
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusFailed || item.Attempts != sendqueue.DefaultMaxAttempts {
		t.Fatalf("after exhausting attempts: %+v, want failed / %d attempts", item, sendqueue.DefaultMaxAttempts)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages, want 0", len(h.sent))
	}
}

func TestQueueSendValidation(t *testing.T) {
	h := newSendQueueTestHarness(t)

	empty := testComposeMessage()
	empty.To = nil
	if _, err := h.svc.QueueSend("acc-1", empty, ""); err == nil || !strings.Contains(err.Error(), "no recipients") {
		t.Fatalf("QueueSend(no recipients) = %v, want a 'no recipients' error", err)
	}
	if _, err := h.svc.QueueSend("missing-account", testComposeMessage(), ""); err == nil || !strings.Contains(err.Error(), "account not found") {
		t.Fatalf("QueueSend(unknown account) = %v, want an 'account not found' error", err)
	}
	if _, err := h.svc.UndoSend("missing-queue-id"); err == nil {
		t.Fatalf("UndoSend(unknown id) succeeded, want an error")
	}
	stats, err := h.svc.GetSendQueueStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 0 {
		t.Fatalf("stats after failed queues = %+v, want empty", stats)
	}
}

func TestQueueReactionThenDispatch(t *testing.T) {
	h := newSendQueueTestHarness(t)
	raw := []byte("From: me@example.com\r\nTo: alice@example.com\r\n\r\nreaction bytes")
	res, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, raw, "Re: hello", "alice@example.com", "msg-1")
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}
	if res.DelaySeconds != settings.DefaultUndoSendDelay {
		t.Fatalf("DelaySeconds = %d, want %d", res.DelaySeconds, settings.DefaultUndoSendDelay)
	}
	if res.DraftID != "" {
		t.Fatalf("DraftID = %q, want empty (a reaction has no draft)", res.DraftID)
	}
	item := h.mustItem(t, res.QueueID)
	if item.Kind != sendqueue.KindReaction {
		t.Fatalf("kind = %q, want reaction", item.Kind)
	}
	if item.ReactionMessageID != "msg-1" {
		t.Fatalf("reactionMessageId = %q, want msg-1", item.ReactionMessageID)
	}
	if item.Status != sendqueue.StatusPending {
		t.Fatalf("status = %q, want pending", item.Status)
	}

	// Inside the undo window: nothing sends.
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(in window) = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages inside the undo window, want 0", len(h.sent))
	}

	// Past the window: the worker sends the pre-built bytes raw, once.
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after window) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 || string(h.sent[0]) != string(raw) {
		t.Fatalf("sent = %d messages, want 1 with the exact queued bytes", len(h.sent))
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after dispatch = %+v, %v, want nil, nil (row deleted)", got, err)
	}
}

func TestQueueReactionUndoRunsOnUndoHook(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var undone []sendqueue.Item
	h.svc.onUndo = func(item sendqueue.Item) { undone = append(undone, item) }

	res, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, []byte("raw"), "Re: hello", "alice@example.com", "msg-1")
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}
	if _, err := h.svc.UndoSend(res.QueueID); err != nil {
		t.Fatalf("UndoSend: %v", err)
	}
	if len(undone) != 1 || undone[0].Kind != sendqueue.KindReaction || undone[0].ReactionMessageID != "msg-1" {
		t.Fatalf("onUndo called with %+v, want one reaction item for msg-1", undone)
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after undo = %+v, %v, want nil, nil (row deleted)", got, err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages after undo, want 0", len(h.sent))
	}
}

func TestQueueReactionFailedDispatchRetries(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.sendErr = fmt.Errorf("failed to connect to SMTP server: connection refused")
	res, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, []byte("raw"), "Re: hello", "alice@example.com", "msg-1")
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}

	// A transient failure retries with backoff; the row stays (with
	// Retry) and nothing is sent.
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(failing) = %d, %v, want 0, nil", n, err)
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusPending || item.Attempts != 1 {
		t.Fatalf("after transient failure: %+v, want pending / 1 attempt", item)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages, want 0", len(h.sent))
	}

	// After the backoff, with the server back: sends, then deletes.
	h.now = h.now.Add(time.Minute)
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after backoff) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
}

func queuedMessageIDHeader(t *testing.T, raw []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Message-ID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Message-ID:"))
		}
	}
	t.Fatal("no Message-ID header in sent bytes")
	return ""
}

// TestRetriedSendReusesMessageID is the finding-8 regression test:
// the server accepts the first attempt but the client loses the
// acknowledgement (transient error after DATA), so the worker retries
// — the retry must carry the same Message-ID as the first attempt,
// not a freshly minted one, so the two copies are recognisably the
// same mail.
func TestRetriedSendReusesMessageID(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	// The queued row stores the Message-ID assigned at queue time.
	full, err := h.svc.store.GetForSend(res.QueueID)
	if err != nil {
		t.Fatal(err)
	}
	if full == nil {
		t.Fatal("queued item not found")
	}
	var stored smtp.ComposeMessage
	if err := json.Unmarshal([]byte(full.MessageJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MessageID == "" {
		t.Fatal("queued message has no Message-ID stored, want one assigned at queue time")
	}

	// Capture every transmission, including the one the client
	// treats as failed (server accepted, ack lost).
	var transmitted [][]byte
	h.svc.ops.rawSendOverride = func(_ context.Context, _ string, _ []string, raw []byte) error {
		transmitted = append(transmitted, raw)
		if h.sendErr != nil {
			return h.sendErr
		}
		return nil
	}
	h.sendErr = fmt.Errorf("failed to send message: read tcp: i/o timeout")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(lost ack) = %d, %v, want 0, nil", n, err)
	}
	if len(transmitted) != 1 {
		t.Fatalf("transmitted %d messages on first attempt, want 1", len(transmitted))
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusPending || item.Attempts != 1 {
		t.Fatalf("after lost ack: %+v, want pending / 1 attempt", item)
	}

	// After the backoff, with the connection back: the retry sends.
	h.now = h.now.Add(time.Minute + time.Second)
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(retry) = %d, %v, want 1, nil", n, err)
	}
	if len(transmitted) != 2 {
		t.Fatalf("transmitted %d messages, want 2 (first attempt + retry)", len(transmitted))
	}
	firstID := queuedMessageIDHeader(t, transmitted[0])
	retryID := queuedMessageIDHeader(t, transmitted[1])
	if firstID != retryID {
		t.Fatalf("retry Message-ID = %q, want the first attempt's %q", retryID, firstID)
	}
	if firstID != stored.MessageID {
		t.Fatalf("transmitted Message-ID = %q, want the queued %q", firstID, stored.MessageID)
	}

	// Sent rows are deleted: no third copy on a later tick.
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after retry = %+v, %v, want nil, nil (row deleted)", got, err)
	}
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(after success) = %d, %v, want 0, nil", n, err)
	}
	if len(transmitted) != 2 {
		t.Fatalf("transmitted %d messages after success, want 2", len(transmitted))
	}
}

// TestQueueSendRejectsDuplicateDraft is the finding-9 duplicate-send
// regression test: the draft stays visible in Drafts during the undo
// window, so opening it and hitting Send again must be rejected —
// the first queued row already holds the snapshot that will send.
func TestQueueSendRejectsDuplicateDraft(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	if _, err := h.svc.QueueSend("acc-1", testComposeMessage(), res.DraftID); err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Fatalf("second QueueSend for the same draft = %v, want an 'already queued' error", err)
	}
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1 (no second row enqueued)", len(items))
	}

	// The rejection lasts only while the first row is live: after
	// Undo, the same draft can be queued again.
	if _, err := h.svc.UndoSend(res.QueueID); err != nil {
		t.Fatalf("UndoSend: %v", err)
	}
	if _, err := h.svc.QueueSend("acc-1", testComposeMessage(), res.DraftID); err != nil {
		t.Fatalf("QueueSend after undo: %v, want success", err)
	}
}

// editQueuedDraft rewrites the live draft row's body, as if the user
// opened the draft from Drafts and edited it during the undo window.
func (h *sendQueueTestHarness) editQueuedDraft(t *testing.T, draftID, body string) {
	t.Helper()
	d, err := h.draftStore.Get(draftID)
	if err != nil || d == nil {
		t.Fatalf("draft %q not found: %v", draftID, err)
	}
	d.BodyText = body
	if err := h.draftStore.Update(d); err != nil {
		t.Fatalf("updating draft: %v", err)
	}
}

// TestUndoSendRestoresSnapshot: edits made to the live draft during
// the undo window must not survive Undo — the reopened composer
// shows the queued snapshot, the message that was about to send.
func TestUndoSendRestoresSnapshot(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.editQueuedDraft(t, res.DraftID, "edited during the undo window")

	undone, err := h.svc.UndoSend(res.QueueID)
	if err != nil {
		t.Fatalf("UndoSend: %v", err)
	}
	if undone.DraftID != res.DraftID {
		t.Fatalf("UndoSend draft = %q, want %q", undone.DraftID, res.DraftID)
	}
	d, err := h.draftStore.Get(res.DraftID)
	if err != nil || d == nil {
		t.Fatalf("draft after undo not found: %v", err)
	}
	if d.BodyText != "hi there" {
		t.Fatalf("draft body after undo = %q, want the queued snapshot %q", d.BodyText, "hi there")
	}
}

// TestEditedDraftKeptAfterDispatch: edits made during the undo window
// do not change what sends (the queued snapshot sends), and the
// edited draft is not silently deleted by the post-send cleanup —
// it stays in Drafts with the user's edits intact.
func TestEditedDraftKeptAfterDispatch(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)
	h.editQueuedDraft(t, res.DraftID, "edited during the undo window")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
	if sent := string(h.sent[0]); !strings.Contains(sent, "hi there") || strings.Contains(sent, "edited during the undo window") {
		t.Fatalf("sent bytes do not match the queued snapshot: %q", sent)
	}
	d, err := h.draftStore.Get(res.DraftID)
	if err != nil || d == nil {
		t.Fatalf("edited draft was deleted by the send: %v", err)
	}
	if d.BodyText != "edited during the undo window" {
		t.Fatalf("draft body after dispatch = %q, want the user's edit kept", d.BodyText)
	}
}

// TestUneditedDraftDeletedAfterDispatch: the normal path is
// unchanged — when the live draft still matches the queued snapshot,
// dispatch deletes it, as before.
func TestUneditedDraftDeletedAfterDispatch(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if d, err := h.draftStore.Get(res.DraftID); err != nil || d != nil {
		t.Fatalf("draft after dispatch = %+v, %v, want nil, nil (deleted)", d, err)
	}
}

func TestSameDraftContent(t *testing.T) {
	a := testComposeMessage()
	b := testComposeMessage()
	if !sameDraftContent(&a, &b) {
		t.Fatalf("identical messages compare as different")
	}
	if !sameDraftContent(nil, nil) {
		t.Fatalf("nil messages compare as different")
	}
	if sameDraftContent(&a, nil) || sameDraftContent(nil, &b) {
		t.Fatalf("nil vs non-nil compares as same")
	}
	changed := testComposeMessage()
	changed.TextBody = "changed"
	if sameDraftContent(&a, &changed) {
		t.Fatalf("changed body compares as same")
	}
	changed = testComposeMessage()
	changed.Subject = "other"
	if sameDraftContent(&a, &changed) {
		t.Fatalf("changed subject compares as same")
	}
	changed = testComposeMessage()
	changed.To = []smtp.Address{{Address: "bob@example.com"}}
	if sameDraftContent(&a, &changed) {
		t.Fatalf("changed recipient compares as same")
	}
	// Fields the draft round-trip does not carry (From, Message-ID,
	// References, read-receipt flag) must not count as edits.
	changed = testComposeMessage()
	changed.MessageID = "<queued@daak>"
	changed.RequestReadReceipt = true
	changed.References = []string{"<parent@daak>"}
	if !sameDraftContent(&a, &changed) {
		t.Fatalf("round-trip-only fields compare as different")
	}
}

// ReemitPendingCountdowns restores the Undo toast after a restart:
// rows still inside their undo window re-emit sendqueue:queued with
// the seconds actually remaining. Rows whose window already elapsed
// (or that are in retry backoff) get no toast.
func TestReemitPendingCountdowns(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var got []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:queued" {
			got = append(got, data)
		}
	}

	res := h.queue(t)

	// 2s into the 5s window: one event, 3s remaining, same payload
	// the live queue path emits.
	h.now = h.now.Add(2 * time.Second)
	h.svc.ReemitPendingCountdowns()
	if len(got) != 1 {
		t.Fatalf("re-emitted %d queued events, want 1", len(got))
	}
	ev := got[0]
	if ev["queueId"] != res.QueueID || ev["accountId"] != "acc-1" ||
		ev["draftId"] != res.DraftID || ev["subject"] != "hello" ||
		ev["recipient"] != "ada@example.com" || ev["delaySeconds"] != 3 {
		t.Fatalf("event = %+v, want queue %q / acc-1 / draft %q / hello / ada@example.com / 3s",
			ev, res.QueueID, res.DraftID)
	}

	// Past the window: no toast — the row dispatches on the next tick.
	got = nil
	h.now = h.now.Add(4 * time.Second)
	h.svc.ReemitPendingCountdowns()
	if len(got) != 0 {
		t.Fatalf("re-emitted %d queued events past the window, want 0", len(got))
	}
}

func TestReemitPendingCountdownsSkipsBackoffRows(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var got []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:queued" {
			got = append(got, data)
		}
	}

	res := h.queue(t)
	// One failed dispatch attempt, retry due in 60s: still pending,
	// but the undo window is long over — no toast is faked for it.
	if err := h.svc.store.RecordAttempt(res.QueueID, 1, sendqueue.StatusPending,
		h.now.Add(60*time.Second), "connection refused"); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	h.now = h.now.Add(time.Second)
	h.svc.ReemitPendingCountdowns()
	if len(got) != 0 {
		t.Fatalf("re-emitted %d queued events for a backoff row, want 0", len(got))
	}
}

func TestReemitPendingCountdownsIncludesReactions(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var got []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:queued" {
			got = append(got, data)
		}
	}

	_, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"ada@example.com"}, []byte("raw reaction bytes"),
		"Re: hello", "ada@example.com", "msg-1")
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}
	// QueueReaction itself emits one live toast event; only the
	// re-emit after a restart is under test here.
	got = nil
	h.now = h.now.Add(2 * time.Second)
	h.svc.ReemitPendingCountdowns()
	if len(got) != 1 {
		t.Fatalf("re-emitted %d queued events for a reaction, want 1", len(got))
	}
	if got[0]["draftId"] != "" || got[0]["delaySeconds"] != 3 {
		t.Fatalf("reaction event = %+v, want draftId \"\" / 3s", got[0])
	}
}
