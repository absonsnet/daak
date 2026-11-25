package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/absonsnet/daak/internal/sendqueue"
	"github.com/absonsnet/daak/internal/smtp"
)

// Re-audit R27: normal sends dispatched through the send queue must
// be reported to the Mail Merge budget hook at SMTP-accept time —
// exactly once per dispatched message, never for reactions, and
// never for a send that failed. The hook is best-effort by design
// (no error return), so a hook failure cannot fail the send either.

// TestR27NormalSendCallsBudgetHookOnce queues one normal message,
// dispatches it past the undo window, and checks the hook fired
// once with the dispatch time — and that a failed dispatch fires
// nothing.
func TestR27NormalSendCallsBudgetHookOnce(t *testing.T) {
	h := newSendQueueTestHarness(t)
	var recorded []time.Time
	h.svc.onNormalSendAccepted = func(sentAt time.Time) { recorded = append(recorded, sentAt) }

	res := h.queue(t)
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(recorded) != 1 {
		t.Fatalf("budget hook calls = %d, want 1", len(recorded))
	}
	if !recorded[0].Equal(h.now) {
		t.Fatalf("hook sentAt = %v, want dispatch time %v", recorded[0], h.now)
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after dispatch = %+v, %v, want nil, nil (row deleted)", got, err)
	}

	// A failed dispatch must not record anything: the mail never
	// reached the server, so it must not consume budget.
	recorded = nil
	h.sendErr = fmt.Errorf("failed to connect to SMTP server: connection refused")
	res2 := h.queue(t)
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 0 {
		t.Fatalf("failed dispatchDue = %d, %v, want 0, nil", n, err)
	}
	if len(recorded) != 0 {
		t.Fatalf("budget hook calls on failed send = %d, want 0", len(recorded))
	}
	if item := h.mustItem(t, res2.QueueID); item.Status != sendqueue.StatusPending {
		t.Fatalf("failed item status = %q, want pending (retry backoff)", item.Status)
	}
}

// TestR27ReactionSendDoesNotCallBudgetHook is the deliberate R27
// exclusion: reactions ride the same send queue, but they are tiny
// user-initiated messages, not bulk mail — counting them could block
// a Mail Merge run because the user reacted to emails. A dispatched
// reaction must leave the hook untouched.
func TestR27ReactionSendDoesNotCallBudgetHook(t *testing.T) {
	h := newSendQueueTestHarness(t)
	called := 0
	h.svc.onNormalSendAccepted = func(time.Time) { called++ }

	raw := []byte("From: me@example.com\r\nTo: alice@example.com\r\n\r\nreaction bytes")
	res, err := h.svc.QueueReaction("acc-1",
		smtp.Address{Name: "Me", Address: "me@example.com"},
		[]string{"alice@example.com"}, raw, "Re: hello", "alice@example.com", "msg-1")
	if err != nil {
		t.Fatalf("QueueReaction: %v", err)
	}
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent = %d, want 1 reaction", len(h.sent))
	}
	if called != 0 {
		t.Fatalf("budget hook calls for a reaction = %d, want 0", called)
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after dispatch = %+v, %v, want nil, nil (row deleted)", got, err)
	}
}

// TestR27BudgetHookPanicCannotFailSend checks the best-effort
// contract structurally: even a hook that panics must not lose the
// send or its post-send bookkeeping (the row is still deleted, the
// mail still goes out).
func TestR27BudgetHookPanicCannotFailSend(t *testing.T) {
	h := newSendQueueTestHarness(t)
	h.svc.onNormalSendAccepted = func(time.Time) { panic("budget hook exploded") }

	res := h.queue(t)
	h.now = h.now.Add(6 * time.Second)
	if n, err := h.svc.dispatchDue(h.now); err != nil || n != 1 {
		t.Fatalf("dispatchDue = %d, %v, want 1, nil", n, err)
	}
	if len(h.sent) != 1 {
		t.Fatalf("sent = %d, want 1 (the send must survive a panicking hook)", len(h.sent))
	}
	if got, err := h.svc.store.Get(res.QueueID); err != nil || got != nil {
		t.Fatalf("Get after dispatch = %+v, %v, want nil, nil (row deleted)", got, err)
	}
}
