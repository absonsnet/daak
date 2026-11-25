package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// stubBudgetRecorder stands in for the Mail Merge bridge in direct-send
// tests: the real bridge needs a full Service, but recordNormalSendBudget
// only calls MailMerge_RecordNormalSend.
type stubBudgetRecorder struct {
	calls int
	panic bool
}

func (s *stubBudgetRecorder) MailMerge_RecordNormalSend(sentAt time.Time) error {
	if s.panic {
		panic("recorder exploded")
	}
	s.calls++
	return nil
}

// TestRecordNormalSendBudget (fourth-audit finding 39): the helper is
// nil-safe, records exactly once, and a panicking recorder cannot fail
// the send.
func TestRecordNormalSendBudget(t *testing.T) {
	recordNormalSendBudget(nil, time.Now())

	rec := &stubBudgetRecorder{}
	recordNormalSendBudget(rec, time.Now())
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want 1", rec.calls)
	}

	rec.panic = true
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("recordNormalSendBudget propagated a recorder panic: %v", r)
			}
		}()
		recordNormalSendBudget(rec, time.Now())
	}()
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want 1 (panic must not record twice)", rec.calls)
	}
}

// TestDirectSendHookRecordsNormalSend: the direct-send path (App and
// ComposerApp SendMessage) passes recordNormalSendBudget as the
// afterSMTP hook, so a direct send counts toward the Mail Merge hourly
// budget exactly once at SMTP-accept time — and a failed send records
// nothing.
func TestDirectSendHookRecordsNormalSend(t *testing.T) {
	h := newSendQueueTestHarness(t)
	rec := &stubBudgetRecorder{}

	_, err, _ := h.svc.ops.sendMessageWithHook(context.Background(), "acc-1", testComposeMessage(), nil, func(_ []byte) {
		recordNormalSendBudget(rec, time.Now())
	})
	if err != nil {
		t.Fatalf("sendMessageWithHook: %v", err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(h.sent))
	}
	if rec.calls != 1 {
		t.Fatalf("budget recorder calls = %d, want 1", rec.calls)
	}

	// A failed send must not consume budget.
	rec.calls = 0
	h.sendErr = fmt.Errorf("failed to connect to SMTP server: connection refused")
	_, err, _ = h.svc.ops.sendMessageWithHook(context.Background(), "acc-1", testComposeMessage(), nil, func(_ []byte) {
		recordNormalSendBudget(rec, time.Now())
	})
	if err == nil {
		t.Fatal("expected a send error, got nil")
	}
	if rec.calls != 0 {
		t.Fatalf("budget recorder calls on failed send = %d, want 0", rec.calls)
	}
}
