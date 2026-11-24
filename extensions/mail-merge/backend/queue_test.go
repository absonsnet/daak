package backend

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostmm "github.com/absonsnet/daak/internal/mailmerge"
	"github.com/xuri/excelize/v2"
)

// waitForRunStatus polls GetRun until the run reaches one of the
// wanted statuses (or the deadline hits).
func waitForRunStatus(t *testing.T, b *MailMergeBridge, runID string, want ...string) hostmm.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := b.MailMerge_GetRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if run.Status == w {
				return run
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach %v in time", runID, want)
	return hostmm.Run{}
}

// startLaterRun starts a "Send later" run and waits for it to finish
// queueing (status "queued" or "done" when nothing queued).
func startLaterRun(t *testing.T, env *bridgeTestEnv, rows []map[string]string) string {
	t.Helper()
	runID, err := env.bridge.MailMerge_StartRun(hostmm.RunRequest{
		Template: testBridgeTemplate(), Rows: rows,
		DeliverMode: "later", AccountID: "acc1",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, env.bridge, runID, hostmm.RunQueued, hostmm.RunDone)
	return runID
}

// waitForQueueEmpty polls ListQueue until no item is still queued
// (or the deadline hits).
func waitForQueueDrained(t *testing.T, b *MailMergeBridge) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := b.MailMerge_GetQueueStats()
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("queue did not drain in time")
}

func TestQueueLaterRunEnqueueSendUnsent(t *testing.T) {
	env := newBridgeTestEnv(t)
	runID := startLaterRun(t, env, testBridgeRows())

	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != hostmm.RunQueued || run.QueuedCount != 3 || run.SentCount != 0 {
		t.Fatalf("run after queueing %+v", run)
	}
	// Nothing was sent by the run itself.
	if len(env.sender.sent) != 0 {
		t.Fatalf("later run must not send: %v", env.sender.sent)
	}
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("queue items %d", len(items))
	}
	for i, item := range items {
		if item.RunID != runID || item.Status != QueueStatusQueued {
			t.Fatalf("item %d %+v", i, item)
		}
		if item.Recipient != testBridgeRows()[i]["Email"] {
			t.Fatalf("item %d recipient %q", i, item.Recipient)
		}
		// The stored bytes are the final rendered message (loaded
		// via the send path — the list view omits raw bytes).
		full, err := env.bridge.store.GetQueueItemForSend(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if full == nil || !strings.Contains(string(full.RawMessage), "Hello "+testBridgeRows()[i]["FirstName"]) {
			t.Fatalf("item %d raw %q", i, string(full.RawMessage))
		}
	}
	stats, err := env.bridge.MailMerge_GetQueueStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Queued != 3 || stats.Total != 3 || len(stats.Runs) != 1 || stats.Runs[0].Queued != 3 {
		t.Fatalf("stats %+v", stats)
	}
	// Send Unsent (all runs) sends all three via the one reused
	// connection, saves each to Sent, and flips the run to done.
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 3 || res.Remaining != 0 || res.Queued != 0 {
		t.Fatalf("dispatch %+v", res)
	}
	if len(env.sender.sent) != 3 || env.factory.created != 1 {
		t.Fatalf("sent %v connections %d", env.sender.sent, env.factory.created)
	}
	if env.sent.saved != 3 {
		t.Fatalf("saved to Sent %d", env.sent.saved)
	}
	run, err = env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != hostmm.RunDone || run.SentCount != 3 || run.QueuedCount != 0 {
		t.Fatalf("run after dispatch %+v", run)
	}
	items, err = env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	// Sent rows are deleted on success — the run log is the record.
	if len(items) != 0 {
		t.Fatalf("queue after dispatch %+v, want empty", items)
	}
}

func TestQueuePerHourCap(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 2}); err != nil {
		t.Fatal(err)
	}
	runID := startLaterRun(t, env, testBridgeRows())

	// First dispatch: 2 of 3 send; the third stays queued and its
	// next attempt is pushed to when the budget frees (oldest + 1h).
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Remaining != 1 || res.Queued != 1 {
		t.Fatalf("first dispatch %+v", res)
	}
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	// The two sent rows are deleted; only the one still queued
	// remains, its next attempt pushed to when the budget frees
	// (oldest + 1h).
	var remaining *QueuedItem
	if len(items) != 1 || items[0].Status != QueueStatusQueued {
		t.Fatalf("items %+v", items)
	}
	remaining = &items[0]
	if !remaining.NextAttemptAt.Equal(fakeNow.Add(time.Hour)) {
		t.Fatalf("pushed next attempt %v want %v", remaining.NextAttemptAt, fakeNow.Add(time.Hour))
	}
	// Still inside the hour: no more sends.
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 || res.Remaining != 1 {
		t.Fatalf("second dispatch in same hour %+v", res)
	}
	// 61 minutes later the budget is free and the item is due again.
	fakeNow = fakeNow.Add(61 * time.Minute)
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Remaining != 0 {
		t.Fatalf("dispatch after hour %+v", res)
	}
	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != hostmm.RunDone || run.SentCount != 3 {
		t.Fatalf("run %+v", run)
	}
}

func TestQueuePauseResumeCancel(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }

	runID := startLaterRun(t, env, testBridgeRows()[:2])

	// Per-run pause: the background tick (manual=false) sends nothing.
	if err := env.bridge.MailMerge_PauseQueuedRun(runID); err != nil {
		t.Fatal(err)
	}
	res, err := env.bridge.dispatchDue(fakeNow, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 {
		t.Fatalf("paused run tick sent %+v", res)
	}
	// Resume: the tick sends both.
	if err := env.bridge.MailMerge_ResumeQueuedRun(runID); err != nil {
		t.Fatal(err)
	}
	res, err = env.bridge.dispatchDue(fakeNow, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 {
		t.Fatalf("resumed tick %+v", res)
	}

	// Global pause: the tick sends nothing, but an explicit Send
	// Unsent still sends (the user asked for these messages now).
	runID2 := startLaterRun(t, env, testBridgeRows()[:2])
	if err := env.bridge.MailMerge_PauseQueue(); err != nil {
		t.Fatal(err)
	}
	res, err = env.bridge.dispatchDue(fakeNow, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 {
		t.Fatalf("globally paused tick sent %+v", res)
	}
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 {
		t.Fatalf("manual send-unsent while paused %+v", res)
	}
	run2, err := env.bridge.MailMerge_GetRun(runID2)
	if err != nil {
		t.Fatal(err)
	}
	if run2.SentCount != 2 {
		t.Fatalf("paused run sent manually %+v", run2)
	}
	if err := env.bridge.MailMerge_ResumeQueue(); err != nil {
		t.Fatal(err)
	}

	// Cancel a run: its queued items are deleted, never sent, and
	// the run log records them skipped.
	runID3 := startLaterRun(t, env, testBridgeRows()[:2])
	if err := env.bridge.MailMerge_CancelQueuedRun(runID3); err != nil {
		t.Fatal(err)
	}
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.RunID == runID3 {
			t.Fatalf("cancelled item still in queue %+v", item)
		}
	}
	run, err := env.bridge.MailMerge_GetRun(runID3)
	if err != nil {
		t.Fatal(err)
	}
	if run.SkippedCount != 2 || run.QueuedCount != 0 {
		t.Fatalf("cancelled run %+v", run)
	}
	// Cancelling one item only cancels that item.
	runID4 := startLaterRun(t, env, testBridgeRows()[:2])
	items, err = env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	var first string
	for _, item := range items {
		if item.RunID == runID4 {
			first = item.ID
			break
		}
	}
	if err := env.bridge.MailMerge_CancelQueuedItem(first); err != nil {
		t.Fatal(err)
	}
	stats, err := env.bridge.MailMerge_GetQueueStats()
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled rows are deleted, so the stats never count them:
	// only runID4's one surviving item is left in the queue.
	if stats.Total != 1 || stats.Queued != 1 || stats.Cancelled != 0 {
		t.Fatalf("stats %+v", stats)
	}
	// Unknown ids error.
	if err := env.bridge.MailMerge_CancelQueuedItem("no-such-item"); err == nil {
		t.Fatal("expected not-found error")
	}
	if err := env.bridge.MailMerge_PauseQueuedRun("no-such-run"); err == nil {
		t.Fatal("expected not-found error")
	}
}

// TestQueueCancelInFlightItemRefused is the finding-15 regression
// test: cancelling an item already claimed for sending must be
// refused — the row stays 'sending', the run log is not rewritten,
// and the claimed send still goes out.
func TestQueueCancelInFlightItemRefused(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }

	runID := startLaterRun(t, env, testBridgeRows()[:1])
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue items %d, want 1", len(items))
	}
	item := items[0]
	claimed, err := env.bridge.store.ClaimItem(item.ID, fakeNow)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("claim must succeed for a queued item")
	}
	// Cancel is refused while the send is in flight.
	if err := env.bridge.MailMerge_CancelQueuedItem(item.ID); err == nil || !strings.Contains(err.Error(), "already sending") {
		t.Fatalf("cancel in-flight must fail with already-sending, got %v", err)
	}
	// The row is still 'sending' and the run log still says queued
	// — nothing was deleted, nothing rewritten.
	got, err := env.bridge.store.GetQueueItem(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != QueueStatusSending {
		t.Fatalf("item after refused cancel %+v, want sending", got)
	}
	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.QueuedCount != 1 || len(run.Rows) != 1 || run.Rows[0].Status != hostmm.RowQueued {
		t.Fatalf("run after refused cancel %+v, want 1 queued row", run)
	}
	// The claimed send still goes out: the stale-claim reset
	// re-queues it and dispatch sends it.
	if _, err := env.bridge.store.ResetStaleSending(fakeNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 {
		t.Fatalf("dispatch after refused cancel %+v, want 1 sent", res)
	}
	run, err = env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SentCount != 1 || run.QueuedCount != 0 {
		t.Fatalf("run after dispatch %+v, want sent 1", run)
	}
}

// TestQueueCancelRunLeavesInFlightItem is the finding-15 run-level
// case: cancelling a whole run deletes its queued items but leaves
// an in-flight ('sending') item alone — its send proceeds and its
// log row records the real outcome.
func TestQueueCancelRunLeavesInFlightItem(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }

	runID := startLaterRun(t, env, testBridgeRows()[:2])
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("queue items %d, want 2", len(items))
	}
	claimed, err := env.bridge.store.ClaimItem(items[0].ID, fakeNow)
	if err != nil || !claimed {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if err := env.bridge.MailMerge_CancelQueuedRun(runID); err != nil {
		t.Fatal(err)
	}
	// The queued item is gone and skipped in the log; the in-flight
	// item is untouched.
	got, err := env.bridge.store.GetQueueItem(items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Status != QueueStatusSending {
		t.Fatalf("in-flight item after run cancel %+v, want sending", got)
	}
	if gone, err := env.bridge.store.GetQueueItem(items[1].ID); err != nil || gone != nil {
		t.Fatalf("queued item after run cancel %+v err %v, want gone", gone, err)
	}
	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SkippedCount != 1 || run.QueuedCount != 1 {
		t.Fatalf("run after run cancel %+v, want 1 skipped / 1 queued", run)
	}
	// The in-flight send still goes out.
	if _, err := env.bridge.store.ResetStaleSending(fakeNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 {
		t.Fatalf("dispatch after run cancel %+v, want 1 sent", res)
	}
	run, err = env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SentCount != 1 || run.SkippedCount != 1 {
		t.Fatalf("run after dispatch %+v, want sent 1 / skipped 1", run)
	}
}

func TestQueueRetryFailedAndMaxAttempts(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	env.sender.fail["jane@example.com"] = errors.New("452 too many emails")

	rows := testBridgeRows()[:2] // john (ok), jane (fails)
	runID := startLaterRun(t, env, rows)

	// Attempt 1: john sends, jane fails but stays queued (backoff 1m).
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("attempt 1 %+v", res)
	}
	jane := findQueueItem(t, env.bridge, runID, "jane@example.com")
	if jane.Status != QueueStatusQueued || jane.Attempts != 1 {
		t.Fatalf("jane after attempt 1 %+v", jane)
	}
	if !jane.NextAttemptAt.Equal(fakeNow.Add(time.Minute)) {
		t.Fatalf("jane next attempt %v want %v", jane.NextAttemptAt, fakeNow.Add(time.Minute))
	}
	// Before the backoff elapses, nothing is due.
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 {
		t.Fatalf("dispatch inside backoff %+v", res)
	}
	// Attempt 2 (past backoff): still failing, backoff 2m.
	fakeNow = fakeNow.Add(2 * time.Minute)
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 || res.Failed != 0 {
		t.Fatalf("attempt 2 %+v", res)
	}
	jane = findQueueItem(t, env.bridge, runID, "jane@example.com")
	if jane.Attempts != 2 || !jane.NextAttemptAt.Equal(fakeNow.Add(2*time.Minute)) {
		t.Fatalf("jane after attempt 2 %+v", jane)
	}
	// Attempt 3: attempts exhausted → failed, run row failed.
	fakeNow = fakeNow.Add(3 * time.Minute)
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Fatalf("attempt 3 %+v", res)
	}
	jane = findQueueItem(t, env.bridge, runID, "jane@example.com")
	if jane.Status != QueueStatusFailed || jane.Attempts != 3 {
		t.Fatalf("jane after attempt 3 %+v", jane)
	}
	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.FailedCount != 1 {
		t.Fatalf("run %+v", run)
	}
	// No further automatic retries: it stays failed.
	fakeNow = fakeNow.Add(time.Hour)
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 {
		t.Fatalf("failed item must not auto-retry: %+v", res)
	}
	// Retry-failed re-queues it; fixing the SMTP failure lets it send.
	if err := env.bridge.MailMerge_RetryFailedRun(runID); err != nil {
		t.Fatal(err)
	}
	jane = findQueueItem(t, env.bridge, runID, "jane@example.com")
	if jane.Status != QueueStatusQueued || jane.Attempts != 0 {
		t.Fatalf("jane after retry %+v", jane)
	}
	delete(env.sender.fail, "jane@example.com")
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 {
		t.Fatalf("retry dispatch %+v", res)
	}
}

func TestQueueRunnerDispatches(t *testing.T) {
	env := newBridgeTestEnv(t)
	// A fresh bridge starts no runner: the detached composer relies
	// on this (it never calls StartQueueRunner).
	if env.bridge.runnerRunning {
		t.Fatal("bridge must not start a queue runner by itself")
	}
	env.bridge.queueTickIntervalOverride = 10 * time.Millisecond
	startLaterRun(t, env, testBridgeRows()[:2])
	env.bridge.StartQueueRunner()
	env.bridge.StartQueueRunner() // second call is a no-op
	waitForQueueDrained(t, env.bridge)
	if len(env.sender.sent) != 2 {
		t.Fatalf("runner sent %v", env.sender.sent)
	}
	env.bridge.StopQueueRunner()
	if env.bridge.runnerRunning {
		t.Fatal("runner still running after StopQueueRunner")
	}
}

func TestQueueBackoff(t *testing.T) {
	if queueBackoff(1) != time.Minute || queueBackoff(2) != 2*time.Minute || queueBackoff(3) != 4*time.Minute {
		t.Fatalf("backoff 1/2/3: %v %v %v", queueBackoff(1), queueBackoff(2), queueBackoff(3))
	}
	if queueBackoff(10) != time.Hour {
		t.Fatalf("backoff caps at 1h: %v", queueBackoff(10))
	}
}

func TestQueueConfigDefaults(t *testing.T) {
	env := newBridgeTestEnv(t)
	cfg, err := env.bridge.MailMerge_GetQueueConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PerHour != 100 || cfg.Paused {
		t.Fatalf("defaults %+v", cfg)
	}
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 5, Paused: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err = env.bridge.MailMerge_GetQueueConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PerHour != 5 || !cfg.Paused {
		t.Fatalf("saved %+v", cfg)
	}
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 0}); err == nil {
		t.Fatal("expected per-hour validation error")
	}
}

func TestBridgeParseFileXLSX(t *testing.T) {
	env := newBridgeTestEnv(t)
	xlsxPath := filepath.Join(t.TempDir(), "list.xlsx")
	f := excelize.NewFile()
	for r, rec := range [][]string{
		{"FirstName", "Email"},
		{"John", "john@example.com"},
		{"Jane", "jane@example.com"},
	} {
		for c, v := range rec {
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.SetCellValue("Sheet1", cell, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.NewSheet("Second"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Second", "A1", "Email"); err != nil {
		t.Fatal(err)
	}
	if err := f.SetCellValue("Second", "A2", "second@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveAs(xlsxPath); err != nil {
		t.Fatal(err)
	}
	parsed, err := env.bridge.MailMerge_ParseFile(xlsxPath, "xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RowCount != 2 || len(parsed.PreviewRows) != 2 || len(parsed.Sheets) != 2 || parsed.Sheets[0] != "Sheet1" {
		t.Fatalf("preview %+v", parsed)
	}
	full, err := env.bridge.MailMerge_ParseFileFull(xlsxPath, "xlsx", "")
	if err != nil {
		t.Fatal(err)
	}
	if full.RowCount != 2 || len(full.Rows) != 2 || full.Rows[0]["FirstName"] != "John" {
		t.Fatalf("full %+v", full)
	}
	second, err := env.bridge.MailMerge_ParseFileFull(xlsxPath, "xlsx", "Second")
	if err != nil {
		t.Fatal(err)
	}
	if second.RowCount != 1 || second.Rows[0]["Email"] != "second@example.com" {
		t.Fatalf("second sheet %+v", second)
	}
	if _, err := env.bridge.MailMerge_ParseFileFull(xlsxPath, "xlsx", "Nope"); err == nil {
		t.Fatal("expected error for unknown sheet")
	}
	if _, err := env.bridge.MailMerge_ParseFile(xlsxPath, "ods"); err == nil {
		t.Fatal("expected error for unsupported ODS kind")
	}
	if _, err := env.bridge.MailMerge_ParseFileFull("/nonexistent.xlsx", "xlsx", ""); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func findQueueItem(t *testing.T, b *MailMergeBridge, runID, recipient string) QueuedItem {
	t.Helper()
	items, err := b.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.RunID == runID && item.Recipient == recipient {
			return item
		}
	}
	t.Fatalf("no queue item for run %s recipient %s", runID, recipient)
	return QueuedItem{}
}

// TestQueueRetryResendsIdenticalBytes is the finding-8 Mail Merge
// regression test: the queued bytes are rendered once at queue time,
// so a retry after a lost acknowledgement must transmit the exact
// same bytes — same Message-ID — as the first attempt, not a
// freshly rendered copy.
func TestQueueRetryResendsIdenticalBytes(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	env.sender.failAfterSend["john@example.com"] = errors.New("read tcp: i/o timeout")

	runID := startLaterRun(t, env, testBridgeRows()[:1])

	// Attempt 1: the bytes reach the server, the ack is lost.
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 || res.Failed != 0 {
		t.Fatalf("attempt 1 %+v, want nothing sent/failed", res)
	}
	if len(env.sender.sentRaw) != 1 {
		t.Fatalf("recorded transmissions %d, want 1", len(env.sender.sentRaw))
	}
	john := findQueueItem(t, env.bridge, runID, "john@example.com")
	if john.Status != QueueStatusQueued || john.Attempts != 1 {
		t.Fatalf("john after attempt 1 %+v, want queued / 1 attempt", john)
	}

	// After the backoff, with the connection back: the retry sends.
	fakeNow = fakeNow.Add(2 * time.Minute)
	delete(env.sender.failAfterSend, "john@example.com")
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 {
		t.Fatalf("retry dispatch %+v, want 1 sent", res)
	}
	if len(env.sender.sentRaw) != 2 {
		t.Fatalf("recorded transmissions %d, want 2 (first attempt + retry)", len(env.sender.sentRaw))
	}
	if env.sender.sentRaw[0] != env.sender.sentRaw[1] {
		t.Fatalf("retry bytes differ from first attempt:\n%s\n---\n%s", env.sender.sentRaw[0], env.sender.sentRaw[1])
	}
	if got := queueMessageID(env.sender.sentRaw[0]); got == "" {
		t.Fatal("transmitted bytes carry no Message-ID")
	}
	// The queue row is gone and the run log records the send once.
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("queue after retry %+v, want empty", items)
	}
	run, err := env.bridge.MailMerge_GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.SentCount != 1 || run.QueuedCount != 0 {
		t.Fatalf("run after retry %+v, want sent 1 / queued 0", run)
	}
}

func queueMessageID(raw string) string {
	for _, line := range strings.Split(raw, "\r\n") {
		if strings.HasPrefix(line, "Message-ID:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Message-ID:"))
		}
	}
	return ""
}
