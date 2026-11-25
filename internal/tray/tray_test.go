package tray

import (
	"testing"
	"time"
)

func TestDoubleClickDetector(t *testing.T) {
	calls := 0
	d := &doubleClickDetector{interval: 50 * time.Millisecond, onDoubleClick: func() { calls++ }}

	// A lone tap does nothing.
	d.tap()
	if calls != 0 {
		t.Fatalf("single tap fired onDoubleClick, want 0 calls")
	}

	// Two rapid taps fire exactly once.
	d.tap()
	if calls != 1 {
		t.Fatalf("double tap: got %d calls, want 1", calls)
	}

	// A third rapid tap starts a new pair instead of re-firing.
	d.tap()
	if calls != 1 {
		t.Fatalf("third tap: got %d calls, want 1", calls)
	}

	// Two taps further apart than the interval do not fire.
	d2 := &doubleClickDetector{interval: 50 * time.Millisecond, onDoubleClick: func() { calls++ }}
	d2.tap()
	time.Sleep(100 * time.Millisecond)
	d2.tap()
	if calls != 1 {
		t.Fatalf("slow taps: got %d calls, want 1", calls)
	}
}

// waitForReady (used by the Windows start to wait for the tray's
// menu to be registered): a closed channel reports true at once, an
// open one reports false after the timeout.
func TestWaitForReady(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	if !waitForReady(ready, time.Second) {
		t.Fatal("closed channel: got false, want true")
	}

	never := make(chan struct{})
	if waitForReady(never, 10*time.Millisecond) {
		t.Fatal("open channel: got true, want false after timeout")
	}
}

// Start is idempotent and Stop is safe to call twice; neither may hang.
// On Linux without a D-Bus session the library logs and never becomes
// ready — the test only exercises the wrapper's state machine, not the
// desktop environment.
func TestTrayStartStopIdempotent(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr := New()
		tr.Start(nil, nil, func() {}, func() {}, func() {}, func() {}, func() {})
		tr.Start(nil, nil, func() {}, func() {}, func() {}, func() {}, func() {})
		tr.Stop()
		tr.Stop()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Start/Stop did not return within 10s")
	}
}

// Re-audit finding R18: a tray whose run loop has exited must report
// as not alive, so the close paths fall back to a real quit instead of
// hiding the window on the strength of the once-only started flag.
// The exit is simulated by closing done directly: that is exactly the
// state the onExit callback — and, on Windows, the Run-return path —
// leaves behind once the loop has ended.
func TestTrayExitedIsNotAlive(t *testing.T) {
	// A tray that never started is not alive, and Done is nil
	// (a nil channel blocks forever in a select: never exits).
	tr := New()
	if tr.Alive() {
		t.Fatal("never-started tray: got alive, want not alive")
	}
	if tr.Done() != nil {
		t.Fatal("never-started tray: Done() is not nil, want nil")
	}

	// A started tray with its loop still running is alive, and Done
	// stays open.
	tr.mu.Lock()
	tr.started = true
	tr.done = make(chan struct{})
	tr.mu.Unlock()
	if !tr.Alive() {
		t.Fatal("started tray with open done: got not alive, want alive")
	}
	select {
	case <-tr.Done():
		t.Fatal("started tray with open done: Done() closed, want open")
	default:
	}

	// Once the loop has exited, the tray is dead: Alive is false and
	// Done is closed.
	close(tr.done)
	if tr.Alive() {
		t.Fatal("exited tray: got alive, want not alive")
	}
	select {
	case <-tr.Done():
	default:
		t.Fatal("exited tray: Done() still open, want closed")
	}
}

// A later Start after the tray has died returns false: the dead tray
// cannot be restarted in-process, so the caller must not believe it
// is back. A Start while the tray is still alive stays a no-op true.
func TestTrayStartAfterDeathReturnsFalse(t *testing.T) {
	noop := func() {}

	// Live tray: idempotent Start returns true.
	live := New()
	live.mu.Lock()
	live.started = true
	live.done = make(chan struct{})
	live.mu.Unlock()
	if ok := live.Start(nil, nil, noop, noop, noop, noop, noop); !ok {
		t.Fatal("Start on a live tray: got false, want true")
	}

	// Dead tray: Start returns false.
	dead := New()
	dead.mu.Lock()
	dead.started = true
	dead.done = make(chan struct{})
	close(dead.done)
	dead.mu.Unlock()
	if ok := dead.Start(nil, nil, noop, noop, noop, noop, noop); ok {
		t.Fatal("Start on a dead tray: got true, want false")
	}
}
