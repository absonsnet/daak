package backend

import (
	"testing"
	"time"
)

// Re-audit R27: the Mail Merge hourly budget counts Mail Merge mail
// only, unless the CountNormalSends opt-in is on — then normal sends
// dispatched through the host send queue also count toward the same
// budget. The opt-in defaults OFF, and recording a normal send while
// it is off must write nothing.

// TestQueueBudgetIgnoresNormalSendsByDefault is the OFF half of R27:
// two recorded normal sends must not change the remaining budget —
// a 3-row run with a 2/hour cap still sends 2 on the first dispatch.
func TestQueueBudgetIgnoresNormalSendsByDefault(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 2}); err != nil {
		t.Fatal(err)
	}
	cfg, err := env.bridge.MailMerge_GetQueueConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CountNormalSends {
		t.Fatalf("default queue config %+v: CountNormalSends must be off", cfg)
	}
	// Recording while the opt-in is off must write nothing.
	for i := 0; i < 2; i++ {
		if err := env.bridge.MailMerge_RecordNormalSend(fakeNow); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := env.bridge.store.CountExternalSentSince(fakeNow.Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("external sends recorded while opt-in off = %d, %v, want 0, nil", n, err)
	}
	startLaterRun(t, env, testBridgeRows())
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Remaining != 1 {
		t.Fatalf("dispatch with opt-in off %+v, want 2 sent, 1 remaining", res)
	}
}

// TestQueueBudgetCountsNormalSendsWhenOptedIn is the ON half of R27:
// two recorded normal sends exhaust a 2/hour cap, so no Mail Merge
// mail goes out until the window frees.
func TestQueueBudgetCountsNormalSendsWhenOptedIn(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 2, CountNormalSends: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := env.bridge.MailMerge_RecordNormalSend(fakeNow); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := env.bridge.store.CountExternalSentSince(fakeNow.Add(-time.Hour)); err != nil || n != 2 {
		t.Fatalf("external sends = %d, %v, want 2, nil", n, err)
	}
	startLaterRun(t, env, testBridgeRows())
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 || res.Remaining != 3 {
		t.Fatalf("dispatch with budget spent on normal sends %+v, want 0 sent, 3 remaining", res)
	}
	if len(env.sender.sent) != 0 {
		t.Fatalf("nothing must be sent while the budget is spent: %v", env.sender.sent)
	}
	// The still-queued items' next attempt is pushed to when the
	// budget frees: the oldest counted send (a normal send, here)
	// + 1h.
	items, err := env.bridge.MailMerge_ListQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("queue items %d, want 3", len(items))
	}
	for _, item := range items {
		if !item.NextAttemptAt.Equal(fakeNow.Add(time.Hour)) {
			t.Fatalf("item next attempt %v, want %v (oldest counted send + 1h)", item.NextAttemptAt, fakeNow.Add(time.Hour))
		}
	}
	// 61 minutes later the window is free and the run drains.
	fakeNow = fakeNow.Add(61 * time.Minute)
	res, err = env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 2 || res.Remaining != 1 {
		t.Fatalf("dispatch after the hour %+v, want 2 sent, 1 remaining", res)
	}
}

// TestQueueBudgetCountsNormalAndMergeSendsTogether checks the two
// sources add up: one normal send plus one Mail Merge send exhaust a
// 2/hour cap, and a normal send older than the window does not count.
func TestQueueBudgetCountsNormalAndMergeSendsTogether(t *testing.T) {
	env := newBridgeTestEnv(t)
	fakeNow := time.Now().UTC().Truncate(time.Second)
	env.bridge.nowFunc = func() time.Time { return fakeNow }
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 2, CountNormalSends: true}); err != nil {
		t.Fatal(err)
	}
	// A normal send from over an hour ago: outside the window, so
	// it must not count (the next record also prunes it).
	if err := env.bridge.MailMerge_RecordNormalSend(fakeNow.Add(-61 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	// One normal send inside the window.
	if err := env.bridge.MailMerge_RecordNormalSend(fakeNow); err != nil {
		t.Fatal(err)
	}
	startLaterRun(t, env, testBridgeRows())
	// 1 normal + 2 merge would be 3 > 2: only 1 merge mail goes out.
	res, err := env.bridge.MailMerge_SendUnsent()
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Remaining != 2 {
		t.Fatalf("dispatch %+v, want 1 sent, 2 remaining", res)
	}
}

// TestRecordExternalSendPrunesOldRows checks the table stays bounded:
// recording a fresh send prunes rows older than the rolling hour.
func TestRecordExternalSendPrunesOldRows(t *testing.T) {
	env := newBridgeTestEnv(t)
	if _, err := env.bridge.MailMerge_ListRuns(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := env.bridge.store.RecordExternalSend(now.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := env.bridge.store.RecordExternalSend(now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := env.bridge.store.DB().QueryRow(`SELECT COUNT(*) FROM mailmerge_external_sends`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("external sends rows = %d, want 1 (the 2h-old row pruned)", n)
	}
}

// TestQueueConfigCountNormalSendsRoundTrip checks the opt-in survives
// a save/load cycle and that an older saved config (without the
// field) still decodes as off.
func TestQueueConfigCountNormalSendsRoundTrip(t *testing.T) {
	env := newBridgeTestEnv(t)
	if err := env.bridge.MailMerge_SaveQueueConfig(QueueConfig{PerHour: 50, CountNormalSends: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := env.bridge.MailMerge_GetQueueConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PerHour != 50 || !cfg.CountNormalSends {
		t.Fatalf("round-tripped config %+v, want perHour 50 with the opt-in on", cfg)
	}
	// An older config blob without the field decodes as off.
	if err := env.bridge.store.KV().Set("queue:config", `{"perHour": 25, "paused": false}`); err != nil {
		t.Fatal(err)
	}
	cfg, err = env.bridge.MailMerge_GetQueueConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PerHour != 25 || cfg.CountNormalSends {
		t.Fatalf("legacy config %+v, want perHour 25 with the opt-in off", cfg)
	}
}
