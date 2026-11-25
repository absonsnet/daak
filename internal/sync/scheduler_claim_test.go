package sync

import (
	"testing"
	"time"
)

// TestBeginEndAccountSync (fourth-audit finding 55): the claim is the
// mechanism SyncAccountInboxBlocking waits on — a second caller gets
// the running sync's done channel instead of a bare "busy", and the
// channel closes exactly when the sync releases its claim, so the
// waiter can re-check for new mail instead of dropping the event.
func TestBeginEndAccountSync(t *testing.T) {
	s := NewScheduler(nil, nil, nil)

	done, ok := s.beginAccountSync("acc-1")
	if !ok || done == nil {
		t.Fatalf("first beginAccountSync = %v, %v; want a channel, true", done, ok)
	}

	// A second claim fails but hands back the running sync's channel.
	waiting, ok := s.beginAccountSync("acc-1")
	if ok {
		t.Fatal("second beginAccountSync = true, want false (sync already running)")
	}
	if waiting != done {
		t.Fatal("second beginAccountSync returned a different channel, want the running sync's")
	}

	// A waiter on that channel unblocks the moment the sync ends.
	unblocked := make(chan struct{})
	go func() {
		<-waiting
		close(unblocked)
	}()
	select {
	case <-unblocked:
		t.Fatal("waiter unblocked before the sync ended")
	case <-time.After(50 * time.Millisecond):
	}
	s.endAccountSync("acc-1", done)
	select {
	case <-unblocked:
	case <-time.After(time.Second):
		t.Fatal("waiter did not unblock after endAccountSync")
	}

	// The account is claimable again after release.
	if _, ok := s.beginAccountSync("acc-1"); !ok {
		t.Fatal("beginAccountSync after release = false, want true")
	}
}
