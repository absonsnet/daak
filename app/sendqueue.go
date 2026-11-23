package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/absonsnet/daak/internal/draft"
	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/sendqueue"
	"github.com/absonsnet/daak/internal/settings"
	"github.com/absonsnet/daak/internal/smtp"
	"github.com/google/uuid"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// Undo Send: background send + persistent Outbox
// ============================================================================
//
// Every normal send is queued first (QueueSend) and dispatched by a
// background worker after the undo window (default 5s, settings
// KeyUndoSendDelay). The composer closes immediately; a countdown
// toast with Undo shows in the main window, and the Outbox row
// offers Undo too. Undo cancels the queue item and the composer
// reopens with the draft, intact. Reactions ride the same queue
// (QueueReaction): the picker closes at once, the pill is applied
// optimistically, and Undo cancels the queued reaction and removes
// the local record.
//
// A dispatched row is deleted, not kept as history — the Outbox
// holds no sent or cancelled rows; the Sent folder is the record.
// Failed rows stay, with Retry.
//
// The worker runs ONLY in the main process (App.Startup). The
// detached composer shares the same DB file and the same queue, but
// never runs a worker — a second worker would race the main one.
// Cross-process safety comes from Store.Claim (atomic
// pending → sending).
//
// Dispatch re-runs composeOps.sendMessage with the stored
// ComposeMessage, so signing, encryption, Sent saving, contact
// updates and draft cleanup behave exactly like a direct send.
// The draft row is deliberately kept until dispatch: it is the Undo
// payload, and sendMessage deletes it on success.
//
// Retry policy (see internal/sendqueue/policy.go): transient errors
// (connect/timeout/DNS/4xx/server-busy) retry with backoff 1m, 2m,
// 4m, 8m, 16m (capped at 1h), max 5 attempts, then the item is
// failed and stays in the Outbox with Retry. Permanent errors
// (auth failure, receive-only, no recipients, build failure) fail
// the item at once — retrying cannot help.

// QueueSendResult is what QueueSend returns to the frontend: the
// queue id, the draft the Undo path reopens, and the countdown the
// toast ticks down.
type QueueSendResult struct {
	QueueID      string `json:"queueId"`
	DraftID      string `json:"draftId"`
	DelaySeconds int    `json:"delaySeconds"`
	SendAt       string `json:"sendAt"`
}

// UndoSendResult is what UndoSend returns: the draft to reopen.
type UndoSendResult struct {
	QueueID   string `json:"queueId"`
	AccountID string `json:"accountId"`
	DraftID   string `json:"draftId"`
}

// sendQueueService is the host-side Undo Send orchestrator. It is
// constructed once per process (App and ComposerApp each build one
// from their own stores); only App starts the worker. The emit and
// syncSent hooks are main-process-only — the detached composer
// passes nil and notifies the main window over IPC instead (see
// ComposerApp.QueueSend).
type sendQueueService struct {
	store      *sendqueue.Store
	settings   *settings.Store
	ops        *composeOps
	draftStore *draft.Store
	ctx        context.Context
	now        func() time.Time // overridable clock (tests)
	emit       func(event string, data map[string]interface{})
	syncSent   func(accountID string)
	saveDraft  func(accountID string, msg smtp.ComposeMessage, draftID string) (string, error)

	workerMu      sync.Mutex
	workerRunning bool
	workerStop    chan struct{}

	// onUndo runs after a queued item is undone, with the deleted
	// row. The App sets it so undoing a reaction also removes the
	// local reaction record (the pill); a normal send has no
	// reaction to remove. Nil-safe: the detached composer passes none.
	onUndo func(item sendqueue.Item)
}

func (s *sendQueueService) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// QueueSend validates a message, ensures a draft exists for the
// Undo path, and queues the send for dispatch after the undo
// window. It never touches the network: validation failures (no
// recipients, receive-only account, unbuildable message) surface
// here, in the composer, instead of failing silently in the
// background.
func (s *sendQueueService) QueueSend(accountID string, msg smtp.ComposeMessage, draftID string) (QueueSendResult, error) {
	log := logging.WithComponent("sendqueue")
	now := s.clock()

	acc, err := s.ops.accountStore.Get(accountID)
	if err != nil {
		return QueueSendResult{}, fmt.Errorf("failed to get account: %w", err)
	}
	if acc == nil {
		return QueueSendResult{}, fmt.Errorf("account not found: %s", accountID)
	}
	if acc.NoOutgoingServer {
		return QueueSendResult{}, fmt.Errorf("account %q is configured as receive-only (no outgoing server)", acc.Email)
	}
	if len(msg.AllRecipients()) == 0 {
		return QueueSendResult{}, fmt.Errorf("no recipients specified")
	}
	// Build once to catch unbuildable messages up front; the worker
	// rebuilds at dispatch (signing/encryption happen there).
	if _, err := msg.ToRFC822(); err != nil {
		return QueueSendResult{}, fmt.Errorf("failed to build message: %w", err)
	}

	delay, err := s.settings.GetUndoSendDelay()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read undo-send delay, using default")
		delay = settings.DefaultUndoSendDelay
	}

	// The draft is the Undo payload: keep the existing one when it
	// is still there, otherwise save one now so Undo always has a
	// composer to reopen.
	if draftID != "" && s.draftStore != nil {
		if d, err := s.draftStore.Get(draftID); err != nil || d == nil {
			draftID = ""
		}
	}
	if draftID == "" {
		if s.saveDraft == nil {
			return QueueSendResult{}, fmt.Errorf("cannot save draft for undo: no draft saver configured")
		}
		id, err := s.saveDraft(accountID, msg, "")
		if err != nil {
			return QueueSendResult{}, fmt.Errorf("failed to save draft for undo: %w", err)
		}
		draftID = id
	}

	raw, err := json.Marshal(msg)
	if err != nil {
		return QueueSendResult{}, fmt.Errorf("failed to encode message: %w", err)
	}
	recipient := ""
	if len(msg.To) > 0 {
		recipient = msg.To[0].Address
	}
	sendAt := now.Add(time.Duration(delay) * time.Second)
	item := sendqueue.Item{
		ID:          uuid.New().String(),
		AccountID:   accountID,
		DraftID:     draftID,
		Kind:        sendqueue.KindMessage,
		MessageJSON: string(raw),
		Subject:     msg.Subject,
		Recipient:   recipient,
		FromAddress: msg.From.Address,
		To:          msg.AllRecipients(),
		Status:      sendqueue.StatusPending,
		Attempts:    0,
		MaxAttempts: sendqueue.DefaultMaxAttempts,
		SendAt:      sendAt,
		DueAt:       sendAt,
		CreatedAt:   now,
	}
	if err := s.store.Enqueue(item); err != nil {
		return QueueSendResult{}, err
	}

	log.Info().
		Str("queue_id", item.ID).
		Str("account_id", accountID).
		Str("draft_id", draftID).
		Int("delay_seconds", delay).
		Msg("Message queued for background send")

	s.emitEvent("sendqueue:updated", nil)
	return QueueSendResult{
		QueueID:      item.ID,
		DraftID:      draftID,
		DelaySeconds: delay,
		SendAt:       sendAt.Format(time.RFC3339),
	}, nil
}

// QueueReaction queues a pre-built reaction email for background
// send after the undo window. Unlike QueueSend there is no draft:
// Undo removes the local reaction record instead of reopening a
// composer. It never touches the network: validation failures
// surface here, at click time, instead of failing silently in the
// background.
func (s *sendQueueService) QueueReaction(accountID string, from smtp.Address, to []string, raw []byte, subject, recipient, reactionMessageID string) (QueueSendResult, error) {
	log := logging.WithComponent("sendqueue")
	now := s.clock()

	acc, err := s.ops.accountStore.Get(accountID)
	if err != nil {
		return QueueSendResult{}, fmt.Errorf("failed to get account: %w", err)
	}
	if acc == nil {
		return QueueSendResult{}, fmt.Errorf("account not found: %s", accountID)
	}
	if acc.NoOutgoingServer {
		return QueueSendResult{}, fmt.Errorf("account %q is configured as receive-only (no outgoing server)", acc.Email)
	}
	if len(to) == 0 {
		return QueueSendResult{}, fmt.Errorf("no recipients specified")
	}
	if len(raw) == 0 {
		return QueueSendResult{}, fmt.Errorf("no reaction message bytes to queue")
	}

	delay, err := s.settings.GetUndoSendDelay()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to read undo-send delay, using default")
		delay = settings.DefaultUndoSendDelay
	}

	sendAt := now.Add(time.Duration(delay) * time.Second)
	item := sendqueue.Item{
		ID:                uuid.New().String(),
		AccountID:         accountID,
		Kind:              sendqueue.KindReaction,
		RawMessage:        raw,
		ReactionMessageID: reactionMessageID,
		Subject:           subject,
		Recipient:         recipient,
		FromAddress:       from.Address,
		To:                to,
		Status:            sendqueue.StatusPending,
		Attempts:          0,
		MaxAttempts:       sendqueue.DefaultMaxAttempts,
		SendAt:            sendAt,
		DueAt:             sendAt,
		CreatedAt:         now,
	}
	if err := s.store.Enqueue(item); err != nil {
		return QueueSendResult{}, err
	}

	log.Info().
		Str("queue_id", item.ID).
		Str("account_id", accountID).
		Str("reaction_message_id", reactionMessageID).
		Int("delay_seconds", delay).
		Msg("Reaction queued for background send")

	s.emitEvent("sendqueue:updated", nil)
	// Start the Undo countdown toast in the main window: the same
	// event the detached composer uses to hand its toast over.
	s.emitEvent("sendqueue:queued", map[string]interface{}{
		"queueId":      item.ID,
		"accountId":    accountID,
		"draftId":      "",
		"subject":      subject,
		"recipient":    recipient,
		"delaySeconds": delay,
	})
	return QueueSendResult{
		QueueID:      item.ID,
		DraftID:      "",
		DelaySeconds: delay,
		SendAt:       sendAt.Format(time.RFC3339),
	}, nil
}

// UndoSend cancels a queued send that has not dispatched yet and
// returns the draft to reopen. It fails when the item already
// dispatched (sending/sent) — once the SMTP transaction completes
// the mail is gone and cannot be recalled — or when the item is
// already cancelled/failed. A reaction has no draft: undoing it
// deletes the queued send and removes the local reaction record
// (via onUndo), so the pill disappears.
func (s *sendQueueService) UndoSend(queueID string) (UndoSendResult, error) {
	item, err := s.store.Get(queueID)
	if err != nil {
		return UndoSendResult{}, err
	}
	if item == nil {
		return UndoSendResult{}, fmt.Errorf("queued send %q not found", queueID)
	}
	ok, err := s.store.DeletePending(queueID)
	if err != nil {
		return UndoSendResult{}, err
	}
	if !ok {
		switch item.Status {
		case sendqueue.StatusSent, sendqueue.StatusSending:
			return UndoSendResult{}, fmt.Errorf("message already sent — it can no longer be undone")
		case sendqueue.StatusCancelled:
			return UndoSendResult{}, fmt.Errorf("message was already undone")
		default:
			return UndoSendResult{}, fmt.Errorf("message cannot be undone (status %q)", item.Status)
		}
	}
	if s.onUndo != nil {
		s.onUndo(*item)
	}
	log := logging.WithComponent("sendqueue")
	log.Info().
		Str("queue_id", queueID).
		Str("draft_id", item.DraftID).
		Str("kind", item.Kind).
		Msg("Queued send undone, message back in Drafts")
	s.emitEvent("sendqueue:updated", nil)
	return UndoSendResult{QueueID: queueID, AccountID: item.AccountID, DraftID: item.DraftID}, nil
}

// ListSendQueue returns the Outbox rows for normal sends, newest
// first. (Mail Merge rows come from MailMerge_ListQueue.)
func (s *sendQueueService) ListSendQueue() ([]sendqueue.Item, error) {
	return s.store.List()
}

// GetSendQueueStats returns the Outbox badge counts for normal sends.
func (s *sendQueueService) GetSendQueueStats() (sendqueue.Stats, error) {
	return s.store.Stats()
}

// RetrySendQueueItem returns a failed item to the queue, due
// immediately, with a fresh attempt budget.
func (s *sendQueueService) RetrySendQueueItem(queueID string) error {
	item, err := s.store.Get(queueID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("queued send %q not found", queueID)
	}
	ok, err := s.store.Retry(queueID, s.clock())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("queued send cannot be retried (status %q)", item.Status)
	}
	log := logging.WithComponent("sendqueue")
	log.Info().
		Str("queue_id", queueID).
		Msg("Failed send queued for retry")
	s.emitEvent("sendqueue:updated", nil)
	return nil
}

// --- Background worker (main process only) -------------------------

// sendQueueTickInterval is the time between dispatch ticks. One
// second keeps the post-countdown send feeling immediate without
// spinning on the DB.
const sendQueueTickInterval = time.Second

// sendQueueStaleClaimAge is how old a 'sending' claim must be before
// the worker treats it as stale (crashed/killed dispatch) and
// re-queues it.
const sendQueueStaleClaimAge = 10 * time.Minute

// sendQueueDispatchBatch is the per-tick dispatch cap: one tick
// never sends more than this many messages (a quit mid-countdown
// with a large backlog still drains within a few ticks).
const sendQueueDispatchBatch = 50

// StartWorker starts the background dispatch goroutine. Safe to
// call twice (second call is a no-op). MAIN PROCESS ONLY.
func (s *sendQueueService) StartWorker() {
	s.workerMu.Lock()
	if s.workerRunning {
		s.workerMu.Unlock()
		return
	}
	stop := make(chan struct{})
	s.workerStop = stop
	s.workerRunning = true
	s.workerMu.Unlock()

	go func() {
		defer recoverPanic("sendqueue", "dispatch worker")
		// First tick immediately, so messages queued while the app
		// was closed (quit mid-countdown) start sending without
		// waiting a full interval.
		s.dispatchDueTick()
		ticker := time.NewTicker(sendQueueTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.dispatchDueTick()
			}
		}
	}()
}

// StopWorker stops the background worker. Used by tests; the app
// itself never stops it (the process exit ends it).
func (s *sendQueueService) StopWorker() {
	s.workerMu.Lock()
	if !s.workerRunning {
		s.workerMu.Unlock()
		return
	}
	close(s.workerStop)
	s.workerRunning = false
	s.workerMu.Unlock()
}

// dispatchDueTick is one worker tick: dispatch all due items,
// swallowing errors (a tick must never crash the goroutine;
// failures are per-item).
func (s *sendQueueService) dispatchDueTick() {
	defer recoverPanic("sendqueue", "dispatch tick")
	if _, err := s.dispatchDue(s.clock()); err != nil {
		log := logging.WithComponent("sendqueue")
		log.Warn().Err(err).Msg("Send queue tick failed")
	}
}

// dispatchDue sends due queued items. It returns the number sent.
// An item is due when its undo window has elapsed (due_at <= now);
// a failed item's due_at is its next retry time.
func (s *sendQueueService) dispatchDue(now time.Time) (int, error) {
	// Reset claims left by a crashed dispatch before doing anything.
	if _, err := s.store.ResetStaleSending(now.Add(-sendQueueStaleClaimAge)); err != nil {
		return 0, err
	}
	due, err := s.store.ListDue(now, sendQueueDispatchBatch)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, item := range due {
		if s.sendOne(item, now) {
			sent++
		}
	}
	return sent, nil
}

// sendOne claims and sends one queued item. Returns true when the
// message was sent. A failed attempt is recorded with backoff (or
// marked failed at max attempts / on a permanent error).
func (s *sendQueueService) sendOne(item sendqueue.Item, now time.Time) bool {
	log := logging.WithComponent("sendqueue")
	claimed, err := s.store.Claim(item.ID, now)
	if err != nil || !claimed {
		return false // undone, or claimed by another dispatcher; skip
	}
	full, err := s.store.GetForSend(item.ID)
	if err != nil || full == nil {
		// Vanished mid-dispatch: leave it 'sending' — the stale-claim
		// reset re-queues it.
		return false
	}
	// Reactions carry pre-built bytes, not a ComposeMessage.
	if full.Kind == sendqueue.KindReaction {
		return s.sendReactionItem(*full, now)
	}
	var msg smtp.ComposeMessage
	if err := json.Unmarshal([]byte(full.MessageJSON), &msg); err != nil {
		_ = s.store.RecordAttempt(item.ID, sendqueue.DefaultMaxAttempts, sendqueue.StatusFailed, now, fmt.Sprintf("corrupt queued message: %v", err))
		s.emitEvent("sendqueue:updated", nil)
		return false
	}
	var d *draft.Draft
	if full.DraftID != "" && s.draftStore != nil {
		d, _ = s.draftStore.Get(full.DraftID) // nil when already gone; send anyway
	}
	if _, err := s.ops.sendMessage(s.ctx, full.AccountID, msg, d); err != nil {
		s.recordFailure(*full, now, err)
		return false
	}
	// Sent: delete the row instead of keeping it as history. If the
	// delete fails, mark it sent as a fallback — the mail is gone,
	// and a 'sending' row left behind would be re-queued and re-sent
	// by the stale-claim reset. The frontend hides sent rows.
	if ok, err := s.store.Delete(item.ID); err != nil || !ok {
		if merr := s.store.MarkSent(item.ID, now); merr != nil {
			log.Warn().Err(merr).Str("queue_id", item.ID).Msg("Message sent but failed to delete or mark sent")
		}
	}
	log.Info().
		Str("queue_id", item.ID).
		Str("account_id", full.AccountID).
		Str("subject", full.Subject).
		Msg("Queued message sent")
	s.emitEvent("sendqueue:updated", nil)
	s.emitEvent("sendqueue:sent", map[string]interface{}{
		"queueId":   item.ID,
		"accountId": full.AccountID,
		"subject":   full.Subject,
		"recipient": full.Recipient,
	})
	if s.syncSent != nil {
		accountID := full.AccountID
		go func() {
			defer recoverPanic("sendqueue", "sync sent folder")
			s.syncSent(accountID)
		}()
	}
	return true
}

// sendReactionItem dispatches one queued reaction: the pre-built
// bytes go out raw (the same post-build steps the old synchronous
// sendReaction ran), the Sent copy is saved, and the row is deleted.
// A failed attempt is recorded with backoff like a failed send; the
// optimistic pill stays — the local reaction record already exists —
// until the user removes it or a retry succeeds. No "sent" snackbar:
// a reaction is not a message the user wants to open from Sent.
func (s *sendQueueService) sendReactionItem(item sendqueue.Item, now time.Time) bool {
	log := logging.WithComponent("sendqueue")
	if len(item.RawMessage) == 0 {
		_ = s.store.RecordAttempt(item.ID, sendqueue.DefaultMaxAttempts, sendqueue.StatusFailed, now, "queued reaction has no message bytes")
		s.emitEvent("sendqueue:updated", nil)
		return false
	}
	if err := s.ops.sendRawMessage(s.ctx, item.AccountID, item.FromAddress, item.To, item.RawMessage); err != nil {
		s.recordFailure(item, now, err)
		return false
	}
	// Save to Sent via IMAP APPEND exactly like a normal send:
	// skipped when the provider auto-saves Sent mail, and a save
	// failure only warns — the send already succeeded, and the Sent
	// sync will pick the message up later either way.
	if acc, accErr := s.ops.accountStore.Get(item.AccountID); accErr == nil && acc != nil && !providerAutoSavesSentMail(acc.IMAPHost) {
		if err := s.ops.saveToSentFolder(s.ctx, item.AccountID, acc, item.RawMessage); err != nil {
			log.Warn().Err(err).Str("accountID", item.AccountID).Msg("Failed to save reaction to Sent folder")
		}
	}
	// Sent: delete the row instead of keeping it as history (same
	// fallback as a normal send — never re-queue a sent mail).
	if ok, err := s.store.Delete(item.ID); err != nil || !ok {
		if merr := s.store.MarkSent(item.ID, now); merr != nil {
			log.Warn().Err(merr).Str("queue_id", item.ID).Msg("Reaction sent but failed to delete or mark sent")
		}
	}
	log.Info().
		Str("queue_id", item.ID).
		Str("account_id", item.AccountID).
		Str("subject", item.Subject).
		Msg("Queued reaction sent")
	s.emitEvent("sendqueue:updated", nil)
	if s.syncSent != nil {
		accountID := item.AccountID
		go func() {
			defer recoverPanic("sendqueue", "sync sent folder")
			s.syncSent(accountID)
		}()
	}
	return true
}

// recordFailure records one failed dispatch: permanent errors fail
// the item at once, transient errors retry with backoff until the
// attempt budget is exhausted.
func (s *sendQueueService) recordFailure(item sendqueue.Item, now time.Time, sendErr error) {
	log := logging.WithComponent("sendqueue")
	attempts := item.Attempts + 1
	status := sendqueue.StatusPending
	dueAt := now.Add(sendqueue.Backoff(attempts))
	if sendqueue.IsPermanentError(sendErr) || attempts >= item.MaxAttempts {
		status = sendqueue.StatusFailed
	}
	_ = s.store.RecordAttempt(item.ID, attempts, status, dueAt, sendErr.Error())
	log.Warn().Err(sendErr).
		Str("queue_id", item.ID).
		Int("attempts", attempts).
		Str("status", status).
		Msg("Queued send failed")
	s.emitEvent("sendqueue:updated", nil)
}

// emitEvent emits a Wails event when an emit hook is configured
// (main process). Nil-safe: the detached composer passes no hook.
func (s *sendQueueService) emitEvent(event string, data map[string]interface{}) {
	if s.emit != nil {
		s.emit(event, data)
	}
}

// --- Wails-bound surface (App) -------------------------------------

// QueueSend queues a composed message for background send after the
// undo window. See sendQueueService.QueueSend.
func (a *App) QueueSend(accountID string, msg smtp.ComposeMessage, draftID string) (QueueSendResult, error) {
	return a.sendQueue.QueueSend(accountID, msg, draftID)
}

// UndoSend cancels a queued send and returns the draft to reopen.
func (a *App) UndoSend(queueID string) (UndoSendResult, error) {
	return a.sendQueue.UndoSend(queueID)
}

// ListSendQueue returns the Outbox rows for normal sends.
func (a *App) ListSendQueue() ([]sendqueue.Item, error) {
	return a.sendQueue.ListSendQueue()
}

// GetSendQueueStats returns the Outbox badge counts for normal sends.
func (a *App) GetSendQueueStats() (sendqueue.Stats, error) {
	return a.sendQueue.GetSendQueueStats()
}

// RetrySendQueueItem retries a failed queued send now.
func (a *App) RetrySendQueueItem(queueID string) error {
	return a.sendQueue.RetrySendQueueItem(queueID)
}

// GetUndoSendDelay returns the undo window in seconds (5/10/20/30).
func (a *App) GetUndoSendDelay() (int, error) {
	return a.settingsStore.GetUndoSendDelay()
}

// SetUndoSendDelay sets the undo window in seconds (5/10/20/30).
func (a *App) SetUndoSendDelay(seconds int) error {
	return a.settingsStore.SetUndoSendDelay(seconds)
}

// newSendQueueService builds the App-side service: Wails events for
// the frontend, Sent-folder sync after dispatch.
func (a *App) newSendQueueService() *sendQueueService {
	return &sendQueueService{
		store:      sendqueue.NewStore(a.db),
		settings:   a.settingsStore,
		ops:        &a.composeOps,
		draftStore: a.draftStore,
		ctx:        a.ctx,
		emit: func(event string, data map[string]interface{}) {
			wailsRuntime.EventsEmit(a.ctx, event, data)
		},
		syncSent: func(accountID string) {
			_ = a.syncSentFolder(accountID)
		},
		// Undoing a reaction removes its local record (the pill) —
		// there is no draft to reopen. A normal send has no
		// reaction to remove.
		onUndo: func(item sendqueue.Item) {
			if item.Kind != sendqueue.KindReaction || item.ReactionMessageID == "" {
				return
			}
			if a.reactionStore == nil {
				return
			}
			own := a.ownEmailsForAccount(item.AccountID)
			emails := make([]string, 0, len(own))
			for email := range own {
				emails = append(emails, email)
			}
			_, _ = a.reactionStore.RemoveReaction(item.ReactionMessageID, emails)
		},
		saveDraft: func(accountID string, msg smtp.ComposeMessage, draftID string) (string, error) {
			res, err := a.SaveDraft(accountID, msg, draftID)
			if err != nil {
				return "", err
			}
			if res == nil || res.Draft == nil {
				return "", fmt.Errorf("save draft returned no draft")
			}
			return res.Draft.ID, nil
		},
	}
}

// newComposerSendQueueService builds the ComposerApp-side service:
// same queue and settings, no worker, no Wails events, no Sent
// sync — the main process owns all three. QueueSend notifies the
// main window over IPC instead (see ComposerApp.QueueSend).
func newComposerSendQueueService(c *ComposerApp) *sendQueueService {
	return &sendQueueService{
		store:      sendqueue.NewStore(c.db),
		settings:   c.settingsStore,
		ops:        &c.composeOps,
		draftStore: c.draftStore,
		ctx:        c.ctx,
		saveDraft: func(accountID string, msg smtp.ComposeMessage, draftID string) (string, error) {
			d, err := c.SaveDraft(accountID, msg, draftID)
			if err != nil {
				return "", err
			}
			if d == nil {
				return "", fmt.Errorf("save draft returned no draft")
			}
			return d.ID, nil
		},
	}
}

// ensureSendQueueService lazily builds the service for tests that
// construct a partial App; production Startup builds it eagerly.
func (a *App) ensureSendQueueService() *sendQueueService {
	if a.sendQueue == nil {
		a.sendQueue = a.newSendQueueService()
	}
	return a.sendQueue
}
