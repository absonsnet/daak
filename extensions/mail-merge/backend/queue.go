package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/absonsnet/daak/internal/logging"
	hostmm "github.com/absonsnet/daak/internal/mailmerge"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ---------------------------------------------------------------------------
// Outbox queue types (Phase 2)
// ---------------------------------------------------------------------------

// QueueItemStatus values for one queued message. 'sent' and
// 'cancelled' are not statuses a queue row can hold anymore — a row
// is deleted on success or cancel, and the run log is the record.
const (
	QueueStatusQueued  = "queued"
	QueueStatusSending = "sending" // claimed by a dispatcher, transient
	QueueStatusFailed  = "failed"  // attempts exhausted
)

// DefaultMaxQueueAttempts is the send-attempt budget per queued item
// before it is marked failed. Retries use exponential backoff
// (1m, 2m, 4m, … capped at 1h); the item is never retried forever.
const DefaultMaxQueueAttempts = 3

// DefaultQueuePerHour is the scheduled-dispatch budget: at most this
// many queued messages per rolling hour, across all runs. It defaults
// to the safety-cap limit (100).
const DefaultQueuePerHour = 100

// QueuedItem is one rendered message waiting in (or already sent from)
// the outbox queue. RawMessage is the final RFC822 bytes, fixed at
// queue time; it is never sent to the frontend.
type QueuedItem struct {
	ID            string     `json:"id"`
	RunID         string     `json:"runId"`
	RowIndex      int        `json:"rowIndex"`
	AccountID     string     `json:"accountId"`
	Recipient     string     `json:"recipient"`
	Subject       string     `json:"subject"`
	FromAddress   string     `json:"fromAddress"`
	To            []string   `json:"to"`
	RawMessage    []byte     `json:"-"`
	Status        string     `json:"status"` // queued | sending | sent | failed | cancelled
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"maxAttempts"`
	NextAttemptAt time.Time  `json:"nextAttemptAt"`
	LastError     string     `json:"lastError,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	SentAt        *time.Time `json:"sentAt,omitempty"`
}

// QueueConfig is the global queue configuration: the scheduled
// dispatch budget plus the global pause switch. Stored as one JSON
// blob in the extension KV (key "queue:config"); it is global, not
// per-account, because the queue spans accounts.
type QueueConfig struct {
	PerHour int  `json:"perHour"`
	Paused  bool `json:"paused"`
}

// DefaultQueueConfig returns the defaults: 100/hour, not paused.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{PerHour: DefaultQueuePerHour, Paused: false}
}

// QueueRunStats is one run's queue breakdown, for the queue view.
type QueueRunStats struct {
	RunID     string `json:"runId"`
	Queued    int    `json:"queued"`
	Sent      int    `json:"sent"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Paused    bool   `json:"paused"`
}

// QueueStats is the whole queue at a glance.
type QueueStats struct {
	Total     int             `json:"total"`
	Queued    int             `json:"queued"`
	Sent      int             `json:"sent"`
	Failed    int             `json:"failed"`
	Cancelled int             `json:"cancelled"`
	Runs      []QueueRunStats `json:"runs"`
}

// DispatchResult is the outcome of one dispatch pass (background
// runner tick or manual Send Unsent).
type DispatchResult struct {
	Sent      int `json:"sent"`
	Failed    int `json:"failed"`    // items that hit max attempts this pass
	Remaining int `json:"remaining"` // still-queued items (any due time)
	Queued    int `json:"queued"`    // total still-queued items (any due time)
}

// queueBackoff returns the delay before the next attempt after
// `attempts` failed attempts: 1m, 2m, 4m, … capped at 1h.
func queueBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := time.Duration(1<<uint(attempts-1)) * time.Minute
	if d > time.Hour {
		return time.Hour
	}
	return d
}

// ---------------------------------------------------------------------------
// Background queue runner
// ---------------------------------------------------------------------------
//
// The runner runs ONLY in the main process. It is started explicitly
// by App.Startup calling MailMergeBridge.StartQueueRunner — never from
// ComposerApp wiring, never from ensureInit. The detached composer
// hosts the same extension (shared SQLite) and is short-lived; a
// second runner there would race the main runner on the same queue.
// Cross-process safety for manual Send Unsent from the detached
// composer comes from Store.ClaimItem (atomic queued → sending).
//
// The runner ticks every queueTickInterval (default 30s), sends due
// items in batches of at most PerHour per rolling hour, and never
// crashes on SMTP failure: a failed send is recorded per item and
// retried with backoff, bounded by MaxAttempts.

// queueRunnerTickInterval is the default time between dispatch ticks.
const queueRunnerTickInterval = 30 * time.Second

// queueStaleClaimAge is how old a 'sending' claim must be before the
// runner treats it as stale (crashed/killed dispatch) and re-queues it.
const queueStaleClaimAge = 10 * time.Minute

// queueNow returns the bridge's clock (time.Now unless a test overrode
// it via nowFunc).
func (b *MailMergeBridge) queueNow() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.nowFunc != nil {
		return b.nowFunc()
	}
	return time.Now()
}

// queueTickInterval returns the runner tick interval (default 30s;
// tests may set queueTickIntervalOverride for a faster tick).
func (b *MailMergeBridge) queueTickInterval() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.queueTickIntervalOverride > 0 {
		return b.queueTickIntervalOverride
	}
	return queueRunnerTickInterval
}

// queueMaxAttempts returns the per-item attempt budget (default 3).
func (b *MailMergeBridge) queueMaxAttempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.queueMaxAttemptsOverride > 0 {
		return b.queueMaxAttemptsOverride
	}
	return DefaultMaxQueueAttempts
}

// StartQueueRunner starts the background dispatch goroutine. Safe to
// call twice (second call is a no-op). MAIN PROCESS ONLY — called
// from App.Startup; ComposerApp must never call it.
func (b *MailMergeBridge) StartQueueRunner() {
	b.mu.Lock()
	if b.runnerRunning {
		b.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	b.runnerStop = stop
	b.runnerRunning = true
	b.mu.Unlock()

	go func() {
		// First tick immediately, so items queued while the app was
		// closed start sending without waiting a full interval.
		b.dispatchDueTick()
		ticker := time.NewTicker(b.queueTickInterval())
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				b.dispatchDueTick()
			}
		}
	}()
}

// StopQueueRunner stops the background runner. Used by tests; the app
// itself never stops it (the process exit ends it).
func (b *MailMergeBridge) StopQueueRunner() {
	b.mu.Lock()
	if !b.runnerRunning {
		b.mu.Unlock()
		return
	}
	close(b.runnerStop)
	b.runnerRunning = false
	b.mu.Unlock()
}

// dispatchDueTick is one runner tick: dispatch all due items, swallowing
// errors (a tick must never crash the goroutine; failures are per-item).
func (b *MailMergeBridge) dispatchDueTick() {
	if !b.gateEnabled() {
		return
	}
	if _, err := b.dispatchDue(b.queueNow(), "", false); err != nil {
		// No UI surface for a background tick; the next tick retries.
		slog := logging.WithComponent("mailmerge")
		slog.Warn().Err(err).Msg("Mail Merge queue tick failed")
	}
}

// dispatchDue sends due queued items (all runs, or one run when runID
// != ""), honouring the per-hour budget and the pauses. It is the
// single dispatch implementation behind the background runner and both
// Send Unsent actions.
//
// manual=true (explicit Send Unsent) ignores the global and per-run
// pause switches — the user asked for these messages now — but the
// per-hour budget still applies. manual=false is the background
// runner, which honours every pause.
func (b *MailMergeBridge) dispatchDue(now time.Time, runID string, manual bool) (DispatchResult, error) {
	var result DispatchResult
	if !b.gateEnabled() {
		return result, nil
	}
	if err := b.ensureInit(); err != nil {
		return result, err
	}
	cfg, err := b.store.GetQueueConfig()
	if err != nil {
		return result, err
	}
	// Reset claims left by a crashed dispatch before doing anything.
	if _, err := b.store.ResetStaleSending(now.Add(-queueStaleClaimAge)); err != nil {
		return result, err
	}
	stats, err := b.store.QueueStats()
	if err != nil {
		return result, err
	}
	result.Queued = stats.Queued
	if cfg.Paused && !manual {
		result.Remaining, _ = b.store.CountQueued(runID)
		return result, nil
	}
	// Hourly budget: sent in the last rolling hour, across all runs.
	// The budget is global because the provider's rate limit is per
	// account/sender, not per merge run.
	windowStart := now.Add(-time.Hour)
	sentLastHour, err := b.store.CountSentSince(windowStart)
	if err != nil {
		return result, err
	}
	remaining := cfg.PerHour - sentLastHour
	if remaining <= 0 {
		// Budget exhausted: push the due items' next attempt to when
		// the budget frees (oldest sent + 1h), so the persisted
		// schedule shows it and survives a restart.
		if oldest, ok, _ := b.store.OldestSentSince(windowStart); ok {
			_ = b.store.PushDueItemsNextAttempt(now, oldest.Add(time.Hour), runID)
		}
		result.Remaining, _ = b.store.CountQueued(runID)
		return result, nil
	}
	due, err := b.store.ListDueItems(now, remaining, runID, manual)
	if err != nil {
		return result, err
	}
	if len(due) == 0 {
		result.Remaining, _ = b.store.CountQueued(runID)
		return result, nil
	}
	// One reused SMTP connection per account for the whole batch.
	byAccount := map[string][]QueuedItem{}
	var accountOrder []string
	for _, item := range due {
		if _, ok := byAccount[item.AccountID]; !ok {
			accountOrder = append(accountOrder, item.AccountID)
		}
		byAccount[item.AccountID] = append(byAccount[item.AccountID], item)
	}
	affectedRuns := map[string]bool{}
	for _, accountID := range accountOrder {
		items := byAccount[accountID]
		sender, err := b.deps.Service.OpenRawSender(context.Background(), accountID)
		if err != nil {
			// SMTP unreachable for this account: record a failed
			// attempt on every due item (bounded retry, not a crash)
			// and move on to the next account.
			for _, item := range items {
				if b.recordQueueFailure(item, now, err) {
					result.Failed++
				}
				affectedRuns[item.RunID] = true
			}
			continue
		}
		for _, item := range items {
			sent, failed := b.sendOneQueuedItem(sender, item, now)
			if sent {
				result.Sent++
			} else if failed {
				result.Failed++
			}
			affectedRuns[item.RunID] = true
		}
		_ = sender.Close()
	}
	// If the budget is now exhausted and due items remain, push their
	// next attempt to when the budget frees.
	if sentLastHour+result.Sent >= cfg.PerHour {
		if oldest, ok, _ := b.store.OldestSentSince(now.Add(-time.Hour)); ok {
			_ = b.store.PushDueItemsNextAttempt(now, oldest.Add(time.Hour), runID)
		}
	}
	result.Remaining, _ = b.store.CountQueued(runID)
	stats, _ = b.store.QueueStats()
	result.Queued = stats.Queued
	b.emitQueueUpdated()
	for runID := range affectedRuns {
		b.refreshRunCounts(runID)
		b.emitRunUpdated(runID)
	}
	return result, nil
}

// sendOneQueuedItem claims and sends one queued item via the shared
// RawSender. Returns (sent, newlyFailed). A failed attempt is recorded
// with backoff (or marked failed at max attempts); the run's row log
// is updated to match the queue state.
func (b *MailMergeBridge) sendOneQueuedItem(sender *hostmm.RawSender, item QueuedItem, now time.Time) (bool, bool) {
	claimed, err := b.store.ClaimItem(item.ID, now)
	if err != nil || !claimed {
		return false, false // claimed by the other process; skip
	}
	full, err := b.store.GetQueueItemForSend(item.ID)
	if err != nil || full == nil {
		// Vanished mid-dispatch: unclaim by marking failed is wrong;
		// leave it 'sending' — the stale-claim reset re-queues it.
		return false, false
	}
	qm := hostmm.QueuedMessage{
		RowIndex: full.RowIndex, Email: full.Recipient, Subject: full.Subject,
		From: full.FromAddress, To: full.To, Raw: full.RawMessage,
	}
	if err := sender.Send(context.Background(), qm); err != nil {
		newlyFailed := b.recordQueueFailure(*full, now, err)
		return false, newlyFailed
	}
	// The queue row is deleted FIRST, before the run log is written:
	// the row is the only thing a crash can re-queue, and the run
	// log is post-send bookkeeping. A crash between the SMTP accept
	// and the delete still leaves a 'sending' row, but that window
	// no longer includes the run-log write (or the Sent save inside
	// sender.Send) — and the stored bytes were rendered once at
	// queue time, so a redispatch re-sends the identical bytes with
	// the identical Message-ID. If the delete fails, fall back to
	// marking it sent — a sent mail must never be re-queued. Only
	// then is the sent outcome recorded in the run log; a crash in
	// that last step loses a log row, never a duplicate mail.
	if _, err := b.store.DeleteSendingQueueItem(item.ID); err != nil {
		_ = b.store.MarkItemSent(item.ID, now)
	}
	_ = b.store.SaveRowResult(item.RunID, hostmm.RowResult{
		Index: item.RowIndex, Email: item.Recipient,
		Status: hostmm.RowSent, Timestamp: now,
	})
	return true, false
}

// recordQueueFailure records one failed send attempt on an item: bump
// attempts, backoff (or failed at max attempts), and update the run
// log when the item is finally failed. Returns true when the item
// just became failed.
func (b *MailMergeBridge) recordQueueFailure(item QueuedItem, now time.Time, sendErr error) bool {
	attempts := item.Attempts + 1
	maxAttempts := b.queueMaxAttempts()
	status := QueueStatusQueued
	nextAttempt := now.Add(queueBackoff(attempts))
	if attempts >= maxAttempts {
		status = QueueStatusFailed
	}
	_ = b.store.UpdateItemAttempt(item.ID, attempts, status, nextAttempt, sendErr.Error())
	if status == QueueStatusFailed {
		_ = b.store.SaveRowResult(item.RunID, hostmm.RowResult{
			Index: item.RowIndex, Email: item.Recipient,
			Status: hostmm.RowFailed, Error: sendErr.Error(), Timestamp: now,
		})
		return true
	}
	return false
}

// refreshRunCounts reloads a run, recomputes its tallies from the
// persisted rows, and persists them. A "queued" run whose queue has
// fully drained becomes "done" — the queue view, not the run status,
// is where the per-item outcome lives from then on.
func (b *MailMergeBridge) refreshRunCounts(runID string) {
	run, err := b.store.GetRun(runID)
	if err != nil || run == nil {
		return
	}
	run.RecomputeCounts()
	if run.Status == hostmm.RunQueued && run.QueuedCount == 0 {
		run.Status = hostmm.RunDone
	}
	_ = b.store.UpdateRunStatusAndCounts(run)
}

// emitQueueUpdated notifies the frontend that the queue changed.
func (b *MailMergeBridge) emitQueueUpdated() {
	if b.deps.Ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(b.deps.Ctx, "mailmerge:queue-updated")
}

// --- Wails-bound queue surface (MailMerge_*) ------------------------------

// MailMerge_ListQueue returns all queue items, oldest first.
func (b *MailMergeBridge) MailMerge_ListQueue() ([]QueuedItem, error) {
	if !b.gateEnabled() {
		return nil, nil
	}
	if err := b.ensureInit(); err != nil {
		return nil, err
	}
	return b.store.ListQueueItems()
}

// MailMerge_GetQueueStats returns global + per-run queue counts.
func (b *MailMergeBridge) MailMerge_GetQueueStats() (QueueStats, error) {
	if !b.gateEnabled() {
		return QueueStats{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return QueueStats{}, err
	}
	return b.store.QueueStats()
}

// MailMerge_GetQueueConfig returns the global queue config.
func (b *MailMergeBridge) MailMerge_GetQueueConfig() (QueueConfig, error) {
	if !b.gateEnabled() {
		return QueueConfig{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return QueueConfig{}, err
	}
	return b.store.GetQueueConfig()
}

// MailMerge_SaveQueueConfig validates and persists the global queue config.
func (b *MailMergeBridge) MailMerge_SaveQueueConfig(cfg QueueConfig) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	return b.store.SaveQueueConfig(cfg)
}

// MailMerge_SendUnsent sends due queued messages now, across all runs
// (Thunderbird's File → Send Unsent Messages). The per-hour budget
// still applies — items beyond it stay queued for the next hour.
func (b *MailMergeBridge) MailMerge_SendUnsent() (DispatchResult, error) {
	if !b.gateEnabled() {
		return DispatchResult{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return DispatchResult{}, err
	}
	return b.dispatchDue(b.queueNow(), "", true)
}

// MailMerge_SendUnsentRun sends the due queued messages of one run now.
func (b *MailMergeBridge) MailMerge_SendUnsentRun(runID string) (DispatchResult, error) {
	if !b.gateEnabled() {
		return DispatchResult{}, nil
	}
	if err := b.ensureInit(); err != nil {
		return DispatchResult{}, err
	}
	run, err := b.store.GetRun(runID)
	if err != nil {
		return DispatchResult{}, err
	}
	if run == nil {
		return DispatchResult{}, fmt.Errorf("mail-merge: run %q not found", runID)
	}
	return b.dispatchDue(b.queueNow(), runID, true)
}

// MailMerge_PauseQueue pauses scheduled dispatch globally (manual Send
// Unsent still works — it is an explicit user action).
func (b *MailMergeBridge) MailMerge_PauseQueue() error {
	return b.setQueuePaused(true)
}

// MailMerge_ResumeQueue resumes scheduled dispatch globally.
func (b *MailMergeBridge) MailMerge_ResumeQueue() error {
	return b.setQueuePaused(false)
}

func (b *MailMergeBridge) setQueuePaused(paused bool) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	cfg, err := b.store.GetQueueConfig()
	if err != nil {
		return err
	}
	cfg.Paused = paused
	if err := b.store.SaveQueueConfig(cfg); err != nil {
		return err
	}
	b.emitQueueUpdated()
	return nil
}

// MailMerge_PauseQueuedRun pauses scheduled dispatch for one run's
// queued messages.
func (b *MailMergeBridge) MailMerge_PauseQueuedRun(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if err := b.requireRun(runID); err != nil {
		return err
	}
	if err := b.store.SetRunQueuePaused(runID, true); err != nil {
		return err
	}
	b.emitQueueUpdated()
	return nil
}

// MailMerge_ResumeQueuedRun resumes scheduled dispatch for one run.
func (b *MailMergeBridge) MailMerge_ResumeQueuedRun(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if err := b.requireRun(runID); err != nil {
		return err
	}
	if err := b.store.SetRunQueuePaused(runID, false); err != nil {
		return err
	}
	b.emitQueueUpdated()
	return nil
}

// MailMerge_CancelQueuedItem cancels one queued message (never sent):
// the run log records it skipped, then the queue row is deleted. An
// item already claimed for sending is refused ("already sending"):
// its send proceeds regardless, so cancelling it would delete the
// row and then let the send overwrite the log as sent.
func (b *MailMergeBridge) MailMerge_CancelQueuedItem(itemID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	item, err := b.store.GetQueueItem(itemID)
	if err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("mail-merge: queue item %q not found", itemID)
	}
	if item.Status == QueueStatusSending {
		return fmt.Errorf("mail-merge: queue item %q is already sending", itemID)
	}
	ok, err := b.store.DeleteQueueItem(itemID)
	if err != nil {
		return err
	}
	if ok {
		_ = b.store.SaveRowResult(item.RunID, hostmm.RowResult{
			Index: item.RowIndex, Email: item.Recipient,
			Status: hostmm.RowSkipped, Error: "cancelled in outbox queue", Timestamp: b.queueNow(),
		})
		b.refreshRunCounts(item.RunID)
	}
	b.emitQueueUpdated()
	b.emitRunUpdated(item.RunID)
	return nil
}

// MailMerge_CancelQueuedRun cancels all queued messages of a run.
func (b *MailMergeBridge) MailMerge_CancelQueuedRun(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if err := b.requireRun(runID); err != nil {
		return err
	}
	// Update the run log for the cancelled rows first (so the counts
	// recompute correctly), then delete the items.
	items, err := b.store.ListQueueItems()
	if err != nil {
		return err
	}
	now := b.queueNow()
	for _, item := range items {
		if item.RunID == runID && item.Status == QueueStatusQueued {
			_ = b.store.SaveRowResult(runID, hostmm.RowResult{
				Index: item.RowIndex, Email: item.Recipient,
				Status: hostmm.RowSkipped, Error: "cancelled in outbox queue", Timestamp: now,
			})
		}
	}
	if _, err := b.store.DeleteRunQueueItems(runID); err != nil {
		return err
	}
	b.refreshRunCounts(runID)
	b.emitQueueUpdated()
	b.emitRunUpdated(runID)
	return nil
}

// MailMerge_RetryFailedRun returns a run's failed (attempts-exhausted)
// items to the queue, due immediately.
func (b *MailMergeBridge) MailMerge_RetryFailedRun(runID string) error {
	if !b.gateEnabled() {
		return nil
	}
	if err := b.ensureInit(); err != nil {
		return err
	}
	if err := b.requireRun(runID); err != nil {
		return err
	}
	items, err := b.store.ListQueueItems()
	if err != nil {
		return err
	}
	now := b.queueNow()
	for _, item := range items {
		if item.RunID == runID && item.Status == QueueStatusFailed {
			_ = b.store.SaveRowResult(runID, hostmm.RowResult{
				Index: item.RowIndex, Email: item.Recipient,
				Status: hostmm.RowQueued, Timestamp: now,
			})
		}
	}
	if _, err := b.store.RetryFailedRunItems(runID, now); err != nil {
		return err
	}
	b.refreshRunCounts(runID)
	b.emitQueueUpdated()
	b.emitRunUpdated(runID)
	return nil
}

// requireRun errors when a run ID is unknown.
func (b *MailMergeBridge) requireRun(runID string) error {
	run, err := b.store.GetRun(runID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("mail-merge: run %q not found", runID)
	}
	return nil
}
