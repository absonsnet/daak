package app

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	svc         *sendQueueService
	db          *database.DB
	draftStore  *draft.Store
	sent        [][]byte
	sendErr     error
	sentSaveErr error // when non-nil, the Sent-folder save fails (R24 tests)
	now         time.Time
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
		sentSaveOverride: func(_ context.Context, _ string, _ []byte) error { return h.sentSaveErr },
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

func (h *sendQueueTestHarness) queueReaction(t *testing.T, raw []byte, messageID string) QueueSendResult {
	t.Helper()
	res, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, raw, "Re: hello", "alice@example.com", messageID)
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}
	return res
}

// TestQueueReactionReplacesPendingReaction is the R8 regression
// test: picking a second emoji inside the undo window must replace
// the pending reaction mail, not queue a second one — the local
// pill is replaced by Record, so the queued mail must match it,
// and the recipient must receive only the second reaction.
func TestQueueReactionReplacesPendingReaction(t *testing.T) {
	h := newSendQueueTestHarness(t)
	first := h.queueReaction(t, []byte("thumbs-up bytes"), "msg-1")
	second := h.queueReaction(t, []byte("heart bytes"), "msg-1")
	if first.QueueID == second.QueueID {
		t.Fatalf("second reaction returned the same queue id %q, want a new row replacing the old one", second.QueueID)
	}
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1 (the first reaction replaced, not duplicated)", len(items))
	}
	if items[0].ID != second.QueueID {
		t.Fatalf("queued row = %q, want the second reaction %q", items[0].ID, second.QueueID)
	}
	if got, err := h.svc.store.Get(first.QueueID); err != nil || got != nil {
		t.Fatalf("Get(first reaction) = %+v, %v, want nil, nil (replaced row deleted)", got, err)
	}

	// Past the window: exactly one reaction sends, the second one.
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 || string(h.sent[0]) != "heart bytes" {
		t.Fatalf("sent = %d messages, want 1 with the heart bytes", len(h.sent))
	}
}

// TestQueueReactionReplacesPendingReactionInBackoff: a pending
// reaction that already failed once (retry backoff) is replaced
// the same way — it never reached the recipient, so the new emoji
// supersedes it, with a fresh attempt budget.
func TestQueueReactionReplacesPendingReactionInBackoff(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.sendErr = fmt.Errorf("failed to connect to SMTP server: connection refused")
	first := h.queueReaction(t, []byte("thumbs-up bytes"), "msg-1")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(failing) = %d, %v, want 0, nil", n, err)
	}
	item := h.mustItem(t, first.QueueID)
	if item.Status != sendqueue.StatusPending || item.Attempts != 1 {
		t.Fatalf("after transient failure: %+v, want pending / 1 attempt", item)
	}

	second := h.queueReaction(t, []byte("heart bytes"), "msg-1")
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != second.QueueID {
		t.Fatalf("queue rows = %+v, want only the second reaction", items)
	}
	if items[0].Attempts != 0 {
		t.Fatalf("replacement attempts = %d, want 0 (fresh budget)", items[0].Attempts)
	}
	h.sendErr = nil
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 || string(h.sent[0]) != "heart bytes" {
		t.Fatalf("sent = %d messages, want 1 with the heart bytes", len(h.sent))
	}
}

// TestQueueReactionRejectsWhileSending: once a reaction is claimed
// for dispatch it cannot be replaced — the send is already running
// — so a second pick is rejected instead of queueing alongside it.
func TestQueueReactionRejectsWhileSending(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queueReaction(t, []byte("thumbs-up bytes"), "msg-1")
	if ok, err := h.svc.store.Claim(res.QueueID, h.now); err != nil || !ok {
		t.Fatalf("Claim = %v, %v, want true, nil", ok, err)
	}
	if _, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, []byte("heart bytes"), "Re: hello", "alice@example.com", "msg-1"); err == nil || !strings.Contains(err.Error(), "already being sent") {
		t.Fatalf("second QueueReaction while sending = %v, want an 'already being sent' error", err)
	}
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != sendqueue.StatusSending {
		t.Fatalf("queue rows = %+v, want the one sending reaction", items)
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

// TestQueueSendRejectsWhenFailedRowExists is the R9 regression
// test: after a send fails, the row sits in the Outbox as failed
// and the draft is still in Drafts — re-sending from Drafts must
// be rejected and pointed at Retry, not enqueue a second row with
// a new Message-ID that could dispatch alongside a Retry of the
// old row. Retry itself reuses the failed row, so the retried mail
// keeps the original Message-ID.
func TestQueueSendRejectsWhenFailedRowExists(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	full, err := h.svc.store.GetForSend(res.QueueID)
	if err != nil {
		t.Fatal(err)
	}
	var stored smtp.ComposeMessage
	if err := json.Unmarshal([]byte(full.MessageJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MessageID == "" {
		t.Fatal("queued message has no Message-ID stored, want one assigned at queue time")
	}

	// Fail the send permanently: the row stays failed in the Outbox.
	h.sendErr = fmt.Errorf("failed to login to SMTP server: 535 Authentication failed")
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(failing) = %d, %v, want 0, nil", n, err)
	}
	item := h.mustItem(t, res.QueueID)
	if item.Status != sendqueue.StatusFailed {
		t.Fatalf("after permanent failure: %+v, want failed", item)
	}

	// Re-sending the same draft is rejected, with a Retry pointer.
	if _, err := h.svc.QueueSend("acc-1", testComposeMessage(), res.DraftID); err == nil || !strings.Contains(err.Error(), "Retry") {
		t.Fatalf("QueueSend after failure = %v, want an error pointing at Retry", err)
	}
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1 (no second row enqueued)", len(items))
	}

	// Retry is the way back — and it reuses the failed row's
	// Message-ID, so the retried mail is recognisably the same mail.
	if err := h.svc.RetrySendQueueItem(res.QueueID); err != nil {
		t.Fatalf("RetrySendQueueItem: %v", err)
	}
	retried, err := h.svc.store.GetForSend(res.QueueID)
	if err != nil {
		t.Fatal(err)
	}
	var retriedMsg smtp.ComposeMessage
	if err := json.Unmarshal([]byte(retried.MessageJSON), &retriedMsg); err != nil {
		t.Fatal(err)
	}
	if retriedMsg.MessageID != stored.MessageID {
		t.Fatalf("retried Message-ID = %q, want the original %q", retriedMsg.MessageID, stored.MessageID)
	}
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after retry) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
}

// TestRetrySendQueueItemRejectsWhenLiveRowExists: Retry must refuse
// while another live row already exists for the same draft —
// otherwise a Retry plus the live row would dispatch the same
// content twice. (The live row here is seeded directly through the
// store: QueueSend would already refuse to create it while the
// failed row exists, and the partial unique index allows a failed
// row and a live row for the same draft to coexist.)
func TestRetrySendQueueItemRejectsWhenLiveRowExists(t *testing.T) {
	h := newSendQueueTestHarness(t)
	res := h.queue(t)

	h.sendErr = fmt.Errorf("failed to login to SMTP server: 535 Authentication failed")
	h.now = h.now.Add(6 * time.Second)
	if _, err := h.svc.dispatchDue(h.now); err != nil {
		t.Fatal(err)
	}
	if item := h.mustItem(t, res.QueueID); item.Status != sendqueue.StatusFailed {
		t.Fatalf("after permanent failure: %+v, want failed", item)
	}

	live := sendqueue.Item{
		ID:          "q-live",
		AccountID:   "acc-1",
		DraftID:     res.DraftID,
		Kind:        sendqueue.KindMessage,
		MessageJSON: `{"subject":"hi"}`,
		Subject:     "hi",
		Recipient:   "ada@example.com",
		FromAddress: "me@example.com",
		To:          []string{"ada@example.com"},
		Status:      sendqueue.StatusPending,
		MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt:      h.now.Add(5 * time.Second),
		DueAt:       h.now.Add(5 * time.Second),
		CreatedAt:   h.now,
	}
	if err := h.svc.store.Enqueue(live); err != nil {
		t.Fatalf("Enqueue(live row alongside failed row): %v", err)
	}

	if err := h.svc.RetrySendQueueItem(res.QueueID); err == nil || !strings.Contains(err.Error(), "live queued send") {
		t.Fatalf("RetrySendQueueItem with a live row = %v, want a 'live queued send' error", err)
	}
	if item := h.mustItem(t, res.QueueID); item.Status != sendqueue.StatusFailed {
		t.Fatalf("failed row after refused retry: %+v, want still failed", item)
	}
	if item := h.mustItem(t, "q-live"); item.Status != sendqueue.StatusPending {
		t.Fatalf("live row after refused retry: %+v, want still pending", item)
	}
}

// TestRetryReactionRejectsWhenLiveReactionExists: the same guard in
// the other direction for reactions — retrying a failed reaction
// while a newer live reaction is queued for the same message would
// send both emojis.
func TestRetryReactionRejectsWhenLiveReactionExists(t *testing.T) {
	h := newSendQueueTestHarness(t)
	failed := sendqueue.Item{
		ID: "r-failed", AccountID: "acc-1", Kind: sendqueue.KindReaction,
		RawMessage: []byte("thumbs-up bytes"), ReactionMessageID: "msg-1",
		Subject: "Re: hello", Recipient: "alice@example.com",
		FromAddress: "me@example.com", To: []string{"alice@example.com"},
		Status: sendqueue.StatusFailed, MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt: h.now, DueAt: h.now, CreatedAt: h.now,
	}
	live := failed
	live.ID = "r-live"
	live.RawMessage = []byte("heart bytes")
	live.Status = sendqueue.StatusPending
	for _, it := range []sendqueue.Item{failed, live} {
		if err := h.svc.store.Enqueue(it); err != nil {
			t.Fatalf("Enqueue(%s): %v", it.ID, err)
		}
	}
	if err := h.svc.RetrySendQueueItem("r-failed"); err == nil || !strings.Contains(err.Error(), "live queued reaction") {
		t.Fatalf("RetrySendQueueItem(failed reaction with live reaction) = %v, want a 'live queued reaction' error", err)
	}
	if item := h.mustItem(t, "r-failed"); item.Status != sendqueue.StatusFailed {
		t.Fatalf("failed reaction after refused retry: %+v, want still failed", item)
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

// TestStartupGateHoldsFreshDispatchUntilReemit is the regression test
// for re-audit finding R14. The worker starts in Startup, but the
// Undo countdowns are only re-emitted later, from
// NotifyStartupComplete — so a row due in the first seconds after
// launch used to dispatch before its toast ever appeared, and the
// undo window was lost. Startup now arms a gate that holds fresh
// (never-attempted) rows until the re-emit runs.
func TestStartupGateHoldsFreshDispatchUntilReemit(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var got []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:queued" {
			got = append(got, data)
		}
	}

	// A row queued just before a restart, still inside its undo window.
	res := h.queue(t)

	// Simulate Startup: the worker starts (gate armed) before the
	// frontend has re-emitted the countdowns.
	h.svc.HoldFreshDispatchUntilCountdownsReemitted()

	// 2s in, still inside the window: the re-emit restores the toast
	// with the time actually left, and the row must not dispatch
	// before that happens.
	h.now = h.now.Add(2 * time.Second)
	h.svc.ReemitPendingCountdowns()
	if len(got) != 1 || got[0]["delaySeconds"] != 3 {
		t.Fatalf("re-emitted %d queued events, want 1 with 3s remaining", len(got))
	}

	// Past the window, gate released by the re-emit: sends.
	h.now = h.now.Add(4 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after window, gate released) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
	if item, err := h.svc.store.Get(res.QueueID); err != nil || item != nil {
		t.Fatalf("row after dispatch = %+v, %v, want deleted", item, err)
	}
}

// TestStartupGateHoldsDueRowUntilReemit covers the sharp end of R14:
// a row whose undo window elapses between Startup and the re-emit
// (a slow restart, or slow settings/theme/extension loading) must
// still not dispatch before the re-emit runs. Its toast is gone —
// the window lapsed — but the gate guarantees the frontend had its
// chance first, and the row dispatches on the next tick after the
// re-emit, as before.
func TestStartupGateHoldsDueRowUntilReemit(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var got []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:queued" {
			got = append(got, data)
		}
	}
	res := h.queue(t)

	h.svc.HoldFreshDispatchUntilCountdownsReemitted()

	// Past the window, re-emit not yet run: must not dispatch.
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("dispatchDue(due, gate held) = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 0 {
		t.Fatalf("sent %d messages before the re-emit, want 0", len(h.sent))
	}
	if item := h.mustItem(t, res.QueueID); item.Status != sendqueue.StatusPending {
		t.Fatalf("row status = %q, want pending (still undoable until the gate releases)", item.Status)
	}

	// The re-emit emits no toast — the window already elapsed — but
	// releases the gate, so the row dispatches on the next tick.
	h.svc.ReemitPendingCountdowns()
	if len(got) != 0 {
		t.Fatalf("re-emitted %d queued events past the window, want 0", len(got))
	}
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(after re-emit) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
}

// TestStartupGateDoesNotHoldRetryRows: the R14 gate holds only fresh
// rows. A row in retry backoff (attempts > 0) is not in an undo
// window, so it must still dispatch while the gate is held.
func TestStartupGateDoesNotHoldRetryRows(t *testing.T) {
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

	// Restart with the gate armed; the backoff elapses before the
	// re-emit runs. The retry must dispatch anyway.
	h.svc.HoldFreshDispatchUntilCountdownsReemitted()
	h.now = h.now.Add(61 * time.Second)
	h.sendErr = nil
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue(retry, gate held) = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}
}

// TestSentSaveFailureLeavesPersistentWarning is the re-audit
// finding R24 regression test: SMTP accepts the mail, the queue row
// is deleted at accept, and then the Sent-folder save fails. The
// delivered mail must not vanish with only a log line — a
// persistent Outbox warning row must record it, naming the subject
// and recipient, and the mail must count as sent (no retry, no
// second copy on a later tick).
func TestSentSaveFailureLeavesPersistentWarning(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.sentSaveErr = fmt.Errorf("failed to append to Sent folder: connection reset")
	var events []map[string]interface{}
	h.svc.emit = func(event string, data map[string]interface{}) {
		if event == "sendqueue:sent-save-failed" {
			events = append(events, data)
		}
	}
	res := h.queue(t)

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil (the mail was delivered)", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1 (SMTP accepted the mail)", len(h.sent))
	}
	// The original queue row is gone (deleted at SMTP accept).
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get(original row) = %+v, %v, want nil, nil (deleted at accept)", got, err)
	}
	// Exactly one warning row remains, with the message's identity.
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1 (the Sent-save warning)", len(items))
	}
	warning := items[0]
	if warning.Status != sendqueue.StatusSentUnsaved {
		t.Fatalf("warning status = %q, want %q", warning.Status, sendqueue.StatusSentUnsaved)
	}
	if warning.Subject != "hello" || warning.Recipient != "ada@example.com" {
		t.Fatalf("warning = %+v, want subject hello / recipient ada@example.com", warning)
	}
	if !strings.Contains(warning.LastError, "Sent save failed") || !strings.Contains(warning.LastError, "connection reset") {
		t.Fatalf("warning lastError = %q, want it to name the Sent save failure", warning.LastError)
	}
	if warning.SentAt == nil {
		t.Fatalf("warning SentAt is nil, want it set (the mail was sent)")
	}
	if len(events) != 1 || events[0]["subject"] != "hello" || events[0]["recipient"] != "ada@example.com" {
		t.Fatalf("sent-save-failed events = %+v, want 1 naming hello / ada@example.com", events)
	}

	// No second copy on a later tick: the warning row is not due.
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("second dispatchDue = %d, %v, want 0, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages after a second tick, want 1", len(h.sent))
	}

	// Retry refuses the warning row: the recipient already has the
	// mail, and retrying would deliver a second copy.
	if err := h.svc.RetrySendQueueItem(warning.ID); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("RetrySendQueueItem(warning) = %v, want an 'already sent' refusal", err)
	}
	if item := h.mustItem(t, warning.ID); item.Status != sendqueue.StatusSentUnsaved {
		t.Fatalf("warning after refused retry: %+v, want still sent_unsaved", item)
	}
}

// TestSentSaveFailureForReactionLeavesWarning is the R24 test for
// the reaction path: a queued reaction whose Sent save fails gets
// the same persistent warning row instead of a log line.
func TestSentSaveFailureForReactionLeavesWarning(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.sentSaveErr = fmt.Errorf("failed to append to Sent folder: connection reset")
	res := h.queueReaction(t, []byte("thumbs-up bytes"), "msg-1")

	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d reactions, want 1", len(h.sent))
	}
	items, err := h.svc.store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue rows = %d, want 1 (the Sent-save warning)", len(items))
	}
	warning := items[0]
	if warning.Status != sendqueue.StatusSentUnsaved || warning.Kind != sendqueue.KindReaction {
		t.Fatalf("warning = %+v, want sent_unsaved reaction", warning)
	}
	if warning.Subject != "Re: hello" || warning.Recipient != "alice@example.com" {
		t.Fatalf("warning = %+v, want subject Re: hello / recipient alice@example.com", warning)
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get(original reaction row) = %+v, %v, want nil, nil (deleted at accept)", got, err)
	}
}

// TestSendMessageReturnsSentSaveError is the R24 unit test for the
// direct-send path: sendMessage reports a failed Sent save as its
// third return instead of swallowing it, while still reporting the
// send itself as successful (SMTP accepted the mail).
func TestSendMessageReturnsSentSaveError(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.sentSaveErr = fmt.Errorf("failed to append to Sent folder: connection reset")

	_, err, sentSaveErr := h.svc.ops.sendMessage(context.Background(), "acc-1", testComposeMessage(), nil)
	if err != nil {
		t.Fatalf("sendMessage err = %v, want nil (SMTP accepted the mail)", err)
	}
	if sentSaveErr == nil || !strings.Contains(sentSaveErr.Error(), "connection reset") {
		t.Fatalf("sendMessage sentSaveErr = %v, want the Sent save failure", sentSaveErr)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(h.sent))
	}

	// With a working Sent save, the third return is nil.
	h.sentSaveErr = nil
	if _, err, sentSaveErr := h.svc.ops.sendMessage(context.Background(), "acc-1", testComposeMessage(), nil); err != nil || sentSaveErr != nil {
		t.Fatalf("sendMessage with a working save = %v, %v, want nil, nil", err, sentSaveErr)
	}
}

// TestQueueReactionConcurrentPicksQueueOnce (fourth-audit finding 13):
// two reactions picked at the same instant — here, two concurrent
// QueueReaction calls for the same message — must leave exactly one
// live reaction row behind, never two that both dispatch.
func TestQueueReactionConcurrentPicksQueueOnce(t *testing.T) {
	h := newSendQueueTestHarness(t)
	raw := []byte("From: me@example.com\r\nTo: alice@example.com\r\n\r\nreaction bytes")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.svc.QueueReaction("acc-1",
				smtp.Address{Name: "Me", Address: "me@example.com"},
				[]string{"alice@example.com"}, raw, "Re: hello", "alice@example.com", "msg-1")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("QueueReaction %d: %v", i, err)
		}
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM send_queue WHERE kind = 'reaction' AND reaction_message_id = 'msg-1' AND status IN ('pending', 'sending')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("live reaction rows for msg-1 = %d, want 1", n)
	}
}

// TestQueueReactionConflictRetryReplacesPending (fourth-audit finding
// 13): when the unique index rejects an insert because another
// process queued a reaction for the same message first, the pending
// row is replaced and the insert retried — the caller never sees a
// raw database error.
func TestQueueReactionConflictRetryReplacesPending(t *testing.T) {
	h := newSendQueueTestHarness(t)
	seed := sendqueue.Item{
		ID: "other-proc", AccountID: "acc-1", Kind: sendqueue.KindReaction,
		ReactionMessageID: "msg-1", RawMessage: []byte("old reaction"),
		Status: sendqueue.StatusPending, MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt: h.now, DueAt: h.now, CreatedAt: h.now,
	}
	if err := h.svc.store.Enqueue(seed); err != nil {
		t.Fatal(err)
	}
	replacement := sendqueue.Item{
		ID: "new-one", AccountID: "acc-1", Kind: sendqueue.KindReaction,
		ReactionMessageID: "msg-1", RawMessage: []byte("new reaction"),
		Status: sendqueue.StatusPending, MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt: h.now, DueAt: h.now, CreatedAt: h.now,
	}
	if err := h.svc.enqueueReactionWithConflictRetry(replacement, "msg-1"); err != nil {
		t.Fatalf("conflict retry: %v", err)
	}
	got, err := h.svc.store.ActiveReaction("msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "new-one" {
		t.Fatalf("active reaction = %+v, want the replacement row new-one", got)
	}
}

// TestQueueReactionConflictRetryRejectsSending: the same conflict
// against a row already being dispatched is a rejection, not a
// replace — a reaction in flight cannot be recalled.
func TestQueueReactionConflictRetryRejectsSending(t *testing.T) {
	h := newSendQueueTestHarness(t)
	seed := sendqueue.Item{
		ID: "sending-one", AccountID: "acc-1", Kind: sendqueue.KindReaction,
		ReactionMessageID: "msg-1", RawMessage: []byte("old reaction"),
		Status: sendqueue.StatusPending, MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt: h.now, DueAt: h.now, CreatedAt: h.now,
	}
	if err := h.svc.store.Enqueue(seed); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE send_queue SET status = 'sending' WHERE id = 'sending-one'`); err != nil {
		t.Fatal(err)
	}
	replacement := sendqueue.Item{
		ID: "new-one", AccountID: "acc-1", Kind: sendqueue.KindReaction,
		ReactionMessageID: "msg-1", RawMessage: []byte("new reaction"),
		Status: sendqueue.StatusPending, MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt: h.now, DueAt: h.now, CreatedAt: h.now,
	}
	err := h.svc.enqueueReactionWithConflictRetry(replacement, "msg-1")
	if err == nil {
		t.Fatal("conflict retry against a sending row should fail")
	}
	if !strings.Contains(err.Error(), "already being sent") {
		t.Fatalf("error %q, want 'already being sent'", err)
	}
}
