package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
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
// the local record. A second reaction for the same message inside
// the undo window replaces the pending row instead of queueing a
// second mail (the pill is replaced too, so the queued mail must
// match it); a reaction already being dispatched is rejected.
// A draft with a failed row in the Outbox cannot be sent again
// from Drafts — Retry on the failed row is the only path back —
// and Retry refuses while another live row exists for the draft,
// so the two paths can never dispatch the same content twice.
//
// A dispatched row is deleted, not kept as history — the Outbox
// holds no sent or cancelled rows; the Sent folder is the record.
// Failed rows stay, with Retry. The one exception is a row whose
// Sent-folder save failed after SMTP accepted the mail (re-audit
// finding R24): it stays as a 'sent_unsaved' warning, because the
// delivered mail has no Sent copy and no other record.
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
// payload, and sendMessage deletes it on success — unless it was
// edited during the undo window, in which case the queued snapshot
// is what sends and the edited draft is kept. Undo restores the
// queued snapshot into the draft row, so the reopened composer
// shows the message that was about to send. A draft with a live
// queue row cannot be queued a second time (QueueSend rejects it).
// The stored ComposeMessage carries the Message-ID assigned at queue
// time, so a retry or a redispatch after a crash rebuilds the same
// mail with the same identity; and the queue row is deleted the
// moment SMTP accepts the mail (via sendMessage's afterSMTP hook),
// before the Sent-folder save — a crash in that post-send work
// cannot re-send the mail.
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

	// reactionMu serialises the replace-if-pending check-then-delete-
	// then-queue in QueueReaction within this process, so two
	// reactions picked at the same instant from two windows cannot
	// both pass the check. The cross-process guard is migration 56's
	// partial unique index on live reaction rows; QueueReaction maps
	// its constraint failure back through the same replace/reject
	// logic (see enqueueReactionWithConflictRetry).
	reactionMu sync.Mutex

	// freshDispatchHeld is the restart gate (re-audit finding R14):
	// while held, dispatchDue skips rows with no dispatch attempt
	// yet (attempts == 0) — rows still inside, or just past, their
	// undo window. It is armed by App.Startup before StartWorker
	// and released by ReemitPendingCountdowns, so no fresh row can
	// dispatch before the frontend has had its chance to show the
	// Undo toast. Retry-backoff rows (attempts > 0) are never held:
	// they are not in an undo window. Zero value is unheld, so
	// services built by tests or the detached composer (which never
	// starts a worker) behave exactly as before.
	gateMu            sync.Mutex
	freshDispatchHeld bool
	gateArmedAt       time.Time

	// onUndo runs after a queued item is undone, with the deleted
	// row. The App sets it so undoing a reaction also removes the
	// local reaction record (the pill); a normal send has no
	// reaction to remove. Nil-safe: the detached composer passes none.
	onUndo func(item sendqueue.Item)

	// onNormalSendAccepted runs at SMTP-accept time for a normal
	// (KindMessage) send dispatched by this worker — inside the
	// afterSMTP hook in sendOne, the moment the server accepts the
	// mail. The App sets it to record the send toward the Mail
	// Merge hourly budget when that opt-in is on (re-audit R27).
	// Best-effort by design: it has no error return, sendOne
	// recovers a panic from it, and a failure inside it can never
	// fail the send. It is NEVER called for reactions
	// (sendReactionItem does not use it): reactions are tiny
	// user-initiated messages, not bulk mail, and counting them
	// could block a Mail Merge run because the user reacted to
	// emails. Nil-safe: tests and the detached composer (which
	// never runs a worker) pass none.
	onNormalSendAccepted func(sentAt time.Time)
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

	// Assign the Message-ID once, here, at queue time. The stored
	// MessageJSON carries it, so every rebuild at dispatch — first
	// attempt, retry, or redispatch after a crash — emits the same
	// Message-ID for the same mail. A fresh id per rebuild would let
	// a crash after SMTP acceptance produce a second copy the server
	// cannot recognise as a duplicate. A caller-supplied id is kept.
	if msg.MessageID == "" {
		msg.MessageID = smtp.NewMessageID()
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

	// A draft that already has a live queue row (still in its undo
	// window, waiting on a retry, or being dispatched) cannot be
	// queued again: the first row already holds the snapshot that
	// will send, and a second row would dispatch a second copy.
	// The draft stays in Drafts during the window, so this is the
	// guard that makes re-opening it and hitting Send harmless.
	// The check above is the user-facing guard; migration 54's
	// partial unique index on live draft rows makes it atomic
	// across processes — a concurrent second insert fails the
	// constraint, and the Enqueue error below is mapped back to
	// the same "already queued" error instead of a raw database
	// error.
	if active, err := s.store.HasActiveForDraft(draftID); err != nil {
		return QueueSendResult{}, err
	} else if active {
		return QueueSendResult{}, fmt.Errorf("draft %q is already queued for send", draftID)
	}

	// A draft with a failed row in the Outbox cannot be sent again
	// from Drafts either: the failed row is the one to Retry, and
	// a second row would carry a new Message-ID, so a Retry of the
	// old row plus a dispatch of the new one would deliver the
	// same content twice. Point the user at Retry instead.
	if failed, err := s.store.HasFailedForDraft(draftID); err != nil {
		return QueueSendResult{}, err
	} else if failed {
		return QueueSendResult{}, fmt.Errorf("draft %q already has a failed send in the Outbox; use Retry on that send instead of sending it again", draftID)
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
		// The partial unique index (migration 54) is the atomic
		// backstop for the check above: a concurrent queue of the
		// same draft fails here, and the user gets the same clear
		// "already queued" message, not a raw database error.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return QueueSendResult{}, fmt.Errorf("draft %q is already queued for send", draftID)
		}
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

	// A reaction for this message may already be queued: the
	// frontend's in-memory guard is per-window and resets as soon
	// as the call returns, so a second pick inside the undo window
	// — or one from another window — would otherwise queue a second
	// reaction mail while Record replaces the local pill, and the
	// recipient would receive both. Replace-if-pending,
	// reject-if-sending: the queued mail must match the pill the
	// user sees, and a reaction already being dispatched cannot be
	// recalled. A pending row in retry backoff is replaced the same
	// way — it never reached the recipient, so the new emoji
	// supersedes it. A failed row is left alone: it stays in the
	// Outbox for Retry and does not block a new reaction.
	//
	// The whole check-then-delete-then-queue is held under
	// reactionMu, so two picks at the same instant in this process
	// serialise instead of both passing the check. The viewer
	// process has its own sendQueueService, so the mutex is not the
	// cross-process guard — migration 56's partial unique index on
	// live reaction rows is, and its constraint failure is mapped
	// back through the same replace/reject logic below.
	s.reactionMu.Lock()
	defer s.reactionMu.Unlock()
	if reactionMessageID != "" {
		active, err := s.store.ActiveReaction(reactionMessageID)
		if err != nil {
			return QueueSendResult{}, err
		}
		if active != nil {
			if active.Status == sendqueue.StatusSending {
				return QueueSendResult{}, fmt.Errorf("reaction for message %q is already being sent", reactionMessageID)
			}
			if n, err := s.store.DeletePendingReaction(reactionMessageID); err != nil {
				return QueueSendResult{}, err
			} else if n > 0 {
				log.Info().
					Str("reaction_message_id", reactionMessageID).
					Int("replaced_rows", n).
					Msg("Replacing pending queued reaction with the new one")
			} else {
				// Nothing was deleted even though a live row was
				// seen a moment ago: re-check before queueing
				// alongside it. If the row was claimed for dispatch
				// in between, it is sending now and the new
				// reaction must be rejected; if it is gone (undone
				// in another window), queueing is safe.
				again, err := s.store.ActiveReaction(reactionMessageID)
				if err != nil {
					return QueueSendResult{}, err
				}
				if again != nil {
					return QueueSendResult{}, fmt.Errorf("reaction for message %q is already being sent", reactionMessageID)
				}
			}
		}
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
		// The partial unique index (migration 56) is the atomic
		// backstop for the check above: a concurrent queue of a
		// reaction for the same message — from the viewer process,
		// which has its own service and mutex — fails here, and
		// the conflict is resolved through the same
		// replace-if-pending / reject-if-sending logic instead of
		// surfacing a raw database error.
		if reactionMessageID != "" && strings.Contains(err.Error(), "UNIQUE constraint failed") {
			if retryErr := s.enqueueReactionWithConflictRetry(item, reactionMessageID); retryErr != nil {
				return QueueSendResult{}, retryErr
			}
		} else {
			return QueueSendResult{}, err
		}
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

// enqueueReactionWithConflictRetry resolves a UNIQUE constraint
// failure from Enqueue: another process queued a reaction for the
// same message between this call's check and its insert. The live row
// is re-checked (ActiveReaction): if it is sending, the new reaction
// is rejected; if it is pending, the other process's row won the
// race — it is deleted and the insert retried once. If the row is
// gone (undone in another window), the insert is retried once. A
// second constraint failure means the race was lost again, and the
// caller reports the message as already queued.
func (s *sendQueueService) enqueueReactionWithConflictRetry(item sendqueue.Item, reactionMessageID string) error {
	active, err := s.store.ActiveReaction(reactionMessageID)
	if err != nil {
		return err
	}
	if active == nil {
		if err := s.store.Enqueue(item); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return fmt.Errorf("reaction for message %q is already queued for send", reactionMessageID)
			}
			return err
		}
		return nil
	}
	if active.Status == sendqueue.StatusSending {
		return fmt.Errorf("reaction for message %q is already being sent", reactionMessageID)
	}
	if _, err := s.store.DeletePendingReaction(reactionMessageID); err != nil {
		return err
	}
	if err := s.store.Enqueue(item); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("reaction for message %q is already queued for send", reactionMessageID)
		}
		return err
	}
	return nil
}

// UndoSend cancels a queued send that has not dispatched yet and
// returns the draft to reopen. It fails when the item already
// dispatched (sending/sent) — once the SMTP transaction completes
// the mail is gone and cannot be recalled — or when the item is
// already cancelled/failed. A reaction has no draft: undoing it
// deletes the queued send and removes the local reaction record
// (via onUndo), so the pill disappears. Undo restores the queued
// snapshot into the draft row, so the reopened composer shows the
// message that was about to send — unless the live draft was edited
// during the undo window, in which case the edited draft is kept
// instead of being overwritten by the snapshot (audit4 finding 19).
func (s *sendQueueService) UndoSend(queueID string) (UndoSendResult, error) {
	// GetForSend, not Get: Undo restores the queued MessageJSON
	// snapshot into the draft row, and Get deliberately omits the
	// message JSON (list view never needs it).
	item, err := s.store.GetForSend(queueID)
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
	// Restore the queued snapshot into the draft row. The draft is
	// the Undo payload, but it is a live row: the user may have
	// edited it during the undo window (it stays visible in Drafts),
	// and Undo must reopen the message that was about to send, not
	// the later edits. The queued MessageJSON is the snapshot taken
	// at queue time, so write it back over the live draft. When the
	// draft no longer exists, saving creates a fresh one — use its
	// id, so the composer reopens the restored content either way.
	// A restore failure must not fail the undo itself: the queued
	// send is already cancelled, and the live draft is still there
	// to reopen.
	draftID := item.DraftID
	if item.Kind == sendqueue.KindMessage && item.MessageJSON != "" && s.saveDraft != nil {
		var snapshot smtp.ComposeMessage
		if err := json.Unmarshal([]byte(item.MessageJSON), &snapshot); err != nil {
			log.Warn().Err(err).Str("queue_id", queueID).Msg("Queued send undone, but the snapshot could not be restored to the draft")
		} else if s.draftDiffersFromSnapshot(item.DraftID, snapshot) {
			// The live draft was edited during the undo window (it
			// stays visible in Drafts while its send counts down).
			// Restoring the snapshot here would silently destroy
			// those edits, so keep the edited draft instead: the
			// composer reopens it as-is, and the queued send is
			// still cancelled.
			log.Info().Str("queue_id", queueID).Str("draft_id", draftID).Msg("Queued send undone; draft was edited during the undo window, keeping the edited draft")
		} else if id, err := s.saveDraft(item.AccountID, snapshot, item.DraftID); err != nil {
			log.Warn().Err(err).Str("queue_id", queueID).Msg("Queued send undone, but the snapshot could not be restored to the draft")
		} else if id != "" {
			draftID = id
		}
	}
	log.Info().
		Str("queue_id", queueID).
		Str("draft_id", draftID).
		Str("kind", item.Kind).
		Msg("Queued send undone, message back in Drafts")
	s.emitEvent("sendqueue:updated", nil)
	return UndoSendResult{QueueID: queueID, AccountID: item.AccountID, DraftID: draftID}, nil
}

// draftDiffersFromSnapshot reports whether the live draft row no longer
// matches the message that was queued — i.e. the user edited it during
// the undo window. Fields compared are the ones saveDraftToDB writes
// straight from the queued message (recipients, reply-to, subject,
// in-reply-to); the body is compared only for unencrypted drafts, whose
// stored body is the message body verbatim. A missing draft (or no
// draft store) counts as "no difference": there is nothing to protect,
// and the snapshot restore recreates the draft.
func (s *sendQueueService) draftDiffersFromSnapshot(draftID string, snapshot smtp.ComposeMessage) bool {
	if s.draftStore == nil || draftID == "" {
		return false
	}
	live, err := s.draftStore.Get(draftID)
	if err != nil || live == nil {
		return false
	}
	if live.Subject != snapshot.Subject {
		return true
	}
	if live.ToList != addressListToJSON(snapshot.To) {
		return true
	}
	if live.CcList != addressListToJSON(snapshot.Cc) {
		return true
	}
	if live.BccList != addressListToJSON(snapshot.Bcc) {
		return true
	}
	if live.ReplyTo != replyToToJSON(snapshot.ReplyTo) {
		return true
	}
	if live.InReplyToID != snapshot.InReplyTo {
		return true
	}
	if !live.Encrypted && !live.PGPEncrypted {
		if live.BodyHTML != snapshot.HTMLBody || live.BodyText != snapshot.TextBody {
			return true
		}
	}
	return false
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

// DeleteSendQueueItem discards a failed normal send from the Outbox
// (audit4 finding 30). A broken queued message used to trap its draft
// forever: it could not be rebuilt, retrying failed the same way, the
// draft could not be re-sent because the failed row blocked it, and
// there was no delete anywhere. Discarding the row unblocks the draft
// (HasFailedForDraft no longer finds it), so the user can edit and
// re-send from Drafts. Only failed rows may be discarded this way:
// pending rows are undone, not deleted, and a sent-save warning is a
// record of a delivered mail, not a failure.
func (s *sendQueueService) DeleteSendQueueItem(queueID string) error {
	item, err := s.store.Get(queueID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("queued send %q not found", queueID)
	}
	if item.Kind != sendqueue.KindMessage {
		return fmt.Errorf("only failed normal sends can be discarded from the Outbox (kind %q)", item.Kind)
	}
	if item.Status != sendqueue.StatusFailed {
		return fmt.Errorf("queued send cannot be discarded (status %q — only failed sends can be discarded)", item.Status)
	}
	ok, err := s.store.Delete(queueID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("queued send %q not found", queueID)
	}
	log := logging.WithComponent("sendqueue")
	log.Info().
		Str("queue_id", queueID).
		Str("draft_id", item.DraftID).
		Str("subject", item.Subject).
		Msg("Failed queued send discarded from the Outbox; its draft can be sent again")
	s.emitEvent("sendqueue:updated", nil)
	return nil
}

// RetrySendQueueItem returns a failed item to the queue, due
// immediately, with a fresh attempt budget. It reuses the failed
// row itself, so the retried mail carries the same Message-ID the
// first attempt did — no new identity is minted. It refuses when
// another live row already exists for the same draft (or, for a
// reaction, the same message): re-sending from Drafts while a
// failed row exists is blocked the other way round (see QueueSend),
// and without this check a Retry plus a live row would dispatch
// the same content twice.
func (s *sendQueueService) RetrySendQueueItem(queueID string) error {
	item, err := s.store.Get(queueID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("queued send %q not found", queueID)
	}
	// A Sent-save warning is not a failed send: the mail was
	// already delivered, only its Sent copy is missing (re-audit
	// finding R24). Retrying it would deliver a second copy to a
	// recipient who already has it, so Retry refuses it outright.
	if item.Status == sendqueue.StatusSentUnsaved {
		return fmt.Errorf("message %q was already sent — only its copy in Sent is missing, so there is nothing to retry; check with the recipient", item.Subject)
	}
	// An unbuildable queued message (audit4 finding 30): its stored
	// bytes cannot even be parsed back into a message, so retrying
	// fails the same way every time. Refuse the retry and point at
	// Discard instead of letting the row spin forever.
	if strings.HasPrefix(item.LastError, "corrupt queued message:") {
		return fmt.Errorf("message %q cannot be rebuilt from the queue (%s) — discard it from the Outbox instead of retrying", item.Subject, item.LastError)
	}
	if item.Kind == sendqueue.KindReaction && item.ReactionMessageID != "" {
		active, err := s.store.ActiveReaction(item.ReactionMessageID)
		if err != nil {
			return err
		}
		if active != nil && active.ID != queueID {
			return fmt.Errorf("message %q already has a live queued reaction", item.ReactionMessageID)
		}
	} else if item.DraftID != "" {
		active, err := s.store.HasActiveForDraft(item.DraftID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("draft %q already has a live queued send", item.DraftID)
		}
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

// sendQueueStartupGateMaxHold bounds the restart gate: the window it
// protects (worker start to NotifyStartupComplete) is seconds, so if
// the frontend never calls back — it never mounts — the gate releases
// itself after a minute rather than stranding fresh mail forever.
// The toast is lost in that pathological case, but the mail sends.
const sendQueueStartupGateMaxHold = time.Minute

// ReemitPendingCountdowns re-emits sendqueue:queued for every queued
// send still inside its undo window, with the seconds remaining.
// Restart recovery: a message queued seconds before a quit would
// otherwise dispatch on the worker's first tick with no toast ever
// shown — no undo opportunity. Re-emitting here (called from
// NotifyStartupComplete, once the frontend is mounted and listening)
// restores the countdown with the time that is actually left.
//
// Only fresh rows qualify: status pending, no dispatch attempt yet,
// due_at still in the future. A row whose window already elapsed
// while the app was down dispatches immediately, as before — its
// undo opportunity lapsed during downtime, and holding it would
// delay mail the user expected sent. A row in retry backoff
// (attempts > 0, due_at pushed out) is not in an undo window at all,
// so no toast is faked for it.
//
// Re-emitting also releases the startup dispatch gate (re-audit
// finding R14): by the time this runs, the frontend is mounted and
// listening, so every row still inside its window has its toast,
// and rows whose window already elapsed may dispatch on the next
// tick. The release happens even when listing fails — holding fresh
// dispatch forever would strand mail, and a listing failure means
// dispatch will surface its own error anyway.
func (s *sendQueueService) ReemitPendingCountdowns() {
	defer s.releaseFreshDispatchGate()
	items, err := s.store.List()
	if err != nil {
		log := logging.WithComponent("sendqueue")
		log.Warn().Err(err).Msg("Could not list queued sends to restore undo countdowns")
		return
	}
	now := s.clock()
	for _, item := range items {
		if item.Status != sendqueue.StatusPending || item.Attempts != 0 {
			continue
		}
		remaining := item.DueAt.Sub(now)
		if remaining <= 0 {
			// Window already elapsed: the worker dispatches it on
			// its next tick, as before.
			continue
		}
		delay := int(math.Ceil(remaining.Seconds()))
		if delay < 1 {
			delay = 1
		}
		s.emitEvent("sendqueue:queued", map[string]interface{}{
			"queueId":      item.ID,
			"accountId":    item.AccountID,
			"draftId":      item.DraftID,
			"subject":      item.Subject,
			"recipient":    item.Recipient,
			"delaySeconds": delay,
		})
	}
}

// HoldFreshDispatchUntilCountdownsReemitted arms the restart gate:
// until ReemitPendingCountdowns runs, dispatchDue skips rows with no
// dispatch attempt yet. Called by App.Startup before StartWorker —
// the worker's first tick is immediate, and without the gate it
// would dispatch a row due in the first seconds after launch before
// NotifyStartupComplete has re-emitted its Undo toast (re-audit
// finding R14). Rows whose undo window already elapsed while the app
// was down are held only until the re-emit runs (seconds), then
// dispatch as before; retry-backoff rows are never held.
func (s *sendQueueService) HoldFreshDispatchUntilCountdownsReemitted() {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	s.freshDispatchHeld = true
	s.gateArmedAt = time.Now()
}

// releaseFreshDispatchGate releases the restart gate. Called by
// ReemitPendingCountdowns (via defer) once the countdowns have been
// re-emitted — including when the frontend mounts while starting
// hidden, since NotifyStartupComplete re-emits there too.
func (s *sendQueueService) releaseFreshDispatchGate() {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	s.freshDispatchHeld = false
}

// freshDispatchBlocked reports whether fresh (never-attempted) rows
// must still be held back. It also enforces the safety valve: if
// the frontend never calls back, the gate releases itself after
// sendQueueStartupGateMaxHold rather than stranding mail forever.
func (s *sendQueueService) freshDispatchBlocked() bool {
	s.gateMu.Lock()
	defer s.gateMu.Unlock()
	if !s.freshDispatchHeld {
		return false
	}
	if time.Since(s.gateArmedAt) > sendQueueStartupGateMaxHold {
		s.freshDispatchHeld = false
		log := logging.WithComponent("sendqueue")
		log.Warn().Msg("Startup countdown gate released by timeout: NotifyStartupComplete never re-emitted countdowns")
		return false
	}
	return true
}

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
	// Restart gate (re-audit finding R14): while the gate is held,
	// fresh rows (no dispatch attempt yet) must not send — their
	// Undo toast has not been shown yet, because
	// ReemitPendingCountdowns has not run. They stay pending and
	// dispatch on the first tick after the gate releases. Retry
	// rows (attempts > 0) are not in an undo window, so they
	// dispatch as normal.
	held := s.freshDispatchBlocked()
	sent := 0
	for _, item := range due {
		if held && item.Attempts == 0 {
			continue
		}
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
	// Send the queued snapshot, never the live draft: the worker
	// already sends msg (unmarshalled from the stored MessageJSON).
	// The live draft row is only the post-send cleanup target — and
	// only when it still matches the snapshot. If the user edited
	// the draft during the undo window, deleting it would silently
	// destroy those edits, so pass nil and leave the edited draft
	// in Drafts. When the draft conversion is unavailable (no
	// draftOps, e.g. in tests), fall back to the old behaviour of
	// passing the draft through.
	if d != nil && s.ops != nil && s.ops.draftOps != nil {
		if live := s.ops.draftOps.toComposeMessage(d); !sameDraftContent(live, &msg) {
			log.Info().Str("queue_id", item.ID).Str("draft_id", d.ID).
				Msg("Queued message sent from its snapshot; live draft was edited during the undo window and is kept")
			d = nil
		}
	}
	// Record SMTP acceptance durably the moment it happens: the
	// hook deletes (or marks sent) the queue row before sendMessage
	// does its post-send work (Sent-folder save, contacts, draft
	// cleanup). A crash in that post-send work must not re-queue
	// a mail the server already accepted. The Delete/MarkSent after
	// sendMessage returns is the fallback for the (already deleted)
	// row — both are idempotent.
	markAccepted := func(_ []byte) {
		if ok, err := s.store.Delete(item.ID); err != nil || !ok {
			if merr := s.store.MarkSent(item.ID, now); merr != nil {
				log.Warn().Err(merr).Str("queue_id", item.ID).Msg("Message accepted by SMTP but failed to delete or mark sent")
			}
		}
		// Counted toward the Mail Merge hourly budget when the
		// opt-in is on (re-audit R27): the bridge method itself
		// no-ops when the opt-in is off or the extension is
		// disabled, and the App's hook drops its error — the mail
		// is already out, so a count write can never fail a send.
		// A panic inside the hook is recovered here so it cannot
		// lose the post-send bookkeeping; the send still succeeds
		// either way. Reactions never reach this hook
		// (sendReactionItem has no such call).
		if s.onNormalSendAccepted != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Warn().Interface("panic", r).Str("queue_id", item.ID).Msg("Normal-send budget hook panicked; send still succeeded")
					}
				}()
				s.onNormalSendAccepted(now)
			}()
		}
	}
	if _, err, sentSaveErr := s.ops.sendMessageWithHook(s.ctx, full.AccountID, msg, d, markAccepted); err != nil {
		s.recordFailure(*full, now, err)
		return false
	} else if sentSaveErr != nil {
		// SMTP accepted the mail — it is delivered — but the
		// Sent-folder save failed, and the queue row is already
		// gone (markAccepted deleted it at accept). Record the
		// persistent warning instead of a log line (re-audit
		// finding R24), and do NOT emit sendqueue:sent: there is
		// no Sent copy for its "View message" action to open.
		// No Sent sync either — there is nothing new in Sent to
		// pick up. The mail counts as sent: no retry, no second
		// copy.
		s.recordSentSaveFailure(*full, now, sentSaveErr)
		return true
	}
	// Sent: the afterSMTP hook above already deleted the row the
	// moment SMTP accepted the mail; this is the fallback for a
	// hook that could not delete it. Either way the row must not
	// survive as 'sending' — the stale-claim reset would re-queue
	// and re-send it. Sent rows are deleted, not kept as history.
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
	// SMTP accepted the reaction: delete (or mark sent) the row
	// BEFORE the Sent-folder save below. A crash during that save
	// must not re-queue a reaction the server already accepted —
	// the pre-built bytes carry a fixed Message-ID, so a redispatch
	// would otherwise send an identical second copy.
	if ok, err := s.store.Delete(item.ID); err != nil || !ok {
		if merr := s.store.MarkSent(item.ID, now); merr != nil {
			log.Warn().Err(merr).Str("queue_id", item.ID).Msg("Reaction sent but failed to delete or mark sent")
		}
	}
	// Save to Sent via IMAP APPEND exactly like a normal send:
	// skipped when the provider auto-saves Sent mail. A save
	// failure used to only warn — the send already succeeded, and
	// the Sent sync will pick the message up later either way — but
	// the queue row is already deleted (above), so a failed save
	// left the delivered reaction with no record anywhere in Daak.
	// Record the persistent warning instead (re-audit finding R24).
	if acc, accErr := s.ops.accountStore.Get(item.AccountID); accErr == nil && acc != nil && !providerAutoSavesSentMail(acc.IMAPHost) {
		if err := s.ops.saveToSentFolder(s.ctx, item.AccountID, acc, item.RawMessage); err != nil {
			s.recordSentSaveFailure(item, now, err)
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

// sameDraftContent reports whether a live draft (converted back to
// a ComposeMessage) still holds exactly the content that was queued.
// Only fields the draft round-trip preserves are compared: the
// draft carries no From, References, read-receipt flag or
// Message-ID, so those are skipped — they cannot tell an edit apart
// from the round-trip itself. Anything uncomparable (an attachment
// whose content cannot be resolved, a body that cannot be
// decrypted) counts as different: the safe direction is to keep the
// draft, never to delete a possibly-edited one.
func sameDraftContent(live, snapshot *smtp.ComposeMessage) bool {
	if live == nil || snapshot == nil {
		return live == snapshot
	}
	if !sameAddresses(live.To, snapshot.To) ||
		!sameAddresses(live.Cc, snapshot.Cc) ||
		!sameAddresses(live.Bcc, snapshot.Bcc) ||
		!sameAddressPtr(live.ReplyTo, snapshot.ReplyTo) ||
		live.Subject != snapshot.Subject ||
		live.TextBody != snapshot.TextBody ||
		live.HTMLBody != snapshot.HTMLBody ||
		live.InReplyTo != snapshot.InReplyTo ||
		live.SignMessage != snapshot.SignMessage ||
		live.PGPSignMessage != snapshot.PGPSignMessage ||
		live.EncryptMessage != snapshot.EncryptMessage ||
		live.PGPEncryptMessage != snapshot.PGPEncryptMessage {
		return false
	}
	if len(live.Attachments) != len(snapshot.Attachments) {
		return false
	}
	for i := range live.Attachments {
		a, b := live.Attachments[i], snapshot.Attachments[i]
		if a.Filename != b.Filename || a.ContentType != b.ContentType ||
			a.Inline != b.Inline || a.ContentID != b.ContentID {
			return false
		}
		ac, aerr := a.ResolveContent()
		bc, berr := b.ResolveContent()
		if aerr != nil || berr != nil || len(ac) != len(bc) {
			return false
		}
	}
	return true
}

func sameAddresses(a, b []smtp.Address) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(strings.TrimSpace(a[i].Address), strings.TrimSpace(b[i].Address)) ||
			a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func sameAddressPtr(a, b *smtp.Address) bool {
	if a == nil || b == nil {
		return a == b
	}
	return strings.EqualFold(strings.TrimSpace(a.Address), strings.TrimSpace(b.Address)) && a.Name == b.Name
}

// RecordMailMergeSentSaveFailure records a persistent "sent but not
// saved to Sent" warning for a Mail Merge send (audit4 finding 24),
// the same warning a normal send records via recordSentSaveFailure.
// The mail was delivered; only its Sent copy is missing, so the
// warning names the subject and recipient for the user to check.
func (s *sendQueueService) RecordMailMergeSentSaveFailure(accountID, subject, recipient, fromAddress string, saveErr error) {
	s.recordSentSaveFailure(sendqueue.Item{
		AccountID:   accountID,
		Kind:        sendqueue.KindMessage,
		Subject:     subject,
		Recipient:   recipient,
		FromAddress: fromAddress,
		To:          []string{recipient},
	}, s.clock(), saveErr)
}

// recordSentSaveFailure records a persistent, user-visible
// warning for a mail that SMTP accepted but whose Sent-folder save
// then failed (re-audit finding R24). The queue row for a queued
// send is already deleted by then (the afterSMTP hook deletes it
// at accept, before the Sent save), and a direct send never had
// one — so without this row the delivered mail would vanish from
// Daak entirely, with only a log line to show for it.
//
// The warning is a new Outbox row with status 'sent_unsaved'
// carrying the subject, recipient and the save error, so the user
// can find it after a restart and check with the recipient. It is
// deliberately NOT a 'failed' row: a failed row offers Retry, and
// retrying this mail would deliver a second copy to someone who
// already has it. The row's DraftID (when present) is traceability
// only — the queue guards (HasActiveForDraft, HasFailedForDraft)
// only look at pending/sending/failed rows, so it never blocks a
// later send. The row also stamps SentAt: the mail was sent, only
// its Sent copy is missing.
//
// Callers emit nothing themselves: this records the row and emits
// sendqueue:updated (Outbox refresh) plus sendqueue:sent-save-failed
// (immediate toast naming the message) here, so every path —
// queued dispatch, reaction dispatch, direct send — warns the
// same way. When the row itself cannot be written, the error is
// logged: there is no further durable place to put the warning.
func (s *sendQueueService) recordSentSaveFailure(item sendqueue.Item, now time.Time, saveErr error) {
	log := logging.WithComponent("sendqueue")
	warning := sendqueue.Item{
		ID:          uuid.New().String(),
		AccountID:   item.AccountID,
		DraftID:     item.DraftID,
		Kind:        item.Kind,
		Subject:     item.Subject,
		Recipient:   item.Recipient,
		FromAddress: item.FromAddress,
		To:          item.To,
		Status:      sendqueue.StatusSentUnsaved,
		Attempts:    item.Attempts,
		MaxAttempts: item.MaxAttempts,
		SendAt:      item.SendAt,
		DueAt:       item.DueAt,
		LastError:   fmt.Sprintf("Sent save failed: %v — message was delivered but no copy was saved to Sent; check with the recipient.", saveErr),
		CreatedAt:   now,
		SentAt:      &now,
	}
	if warning.Kind == "" {
		warning.Kind = sendqueue.KindMessage
	}
	if err := s.store.Enqueue(warning); err != nil {
		log.Error().Err(err).
			Str("account_id", item.AccountID).
			Str("subject", item.Subject).
			Str("recipient", item.Recipient).
			Msg("Message sent but Sent save failed, and the Sent-save warning itself could not be recorded")
		return
	}
	log.Warn().Err(saveErr).
		Str("queue_id", warning.ID).
		Str("account_id", item.AccountID).
		Str("subject", item.Subject).
		Str("recipient", item.Recipient).
		Msg("Message sent but could not be saved to Sent folder; recorded as a Sent-save warning in the Outbox")
	s.emitEvent("sendqueue:updated", nil)
	s.emitEvent("sendqueue:sent-save-failed", map[string]interface{}{
		"queueId":   warning.ID,
		"accountId": item.AccountID,
		"subject":   item.Subject,
		"recipient": item.Recipient,
	})
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

// DeleteSendQueueItem discards a failed normal send from the Outbox,
// unblocking its draft so it can be sent again.
func (a *App) DeleteSendQueueItem(queueID string) error {
	return a.sendQueue.DeleteSendQueueItem(queueID)
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
		// Count a dispatched normal send toward the Mail Merge
		// hourly budget when the R27 opt-in is on. The bridge
		// method no-ops when the opt-in is off or the extension is
		// disabled, and its error is dropped on purpose: the mail
		// is already accepted by SMTP, so a count write can never
		// fail a send. The detached composer never runs a worker,
		// so its service (newComposerSendQueueService) sets no
		// hook — the main process's worker is the single recorder.
		onNormalSendAccepted: func(sentAt time.Time) {
			if a.MailMergeBridge != nil {
				_ = a.MailMergeBridge.MailMerge_RecordNormalSend(sentAt)
			}
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
