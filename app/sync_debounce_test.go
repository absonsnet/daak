package app

import (
	"context"
	"testing"
	"time"
)

// TestSyncFolderDebounceJoinsRunningSync (fourth-audit finding 60): a
// second SyncFolder press within the 500 ms debounce window must wait
// for the running sync's header phase instead of returning success
// without doing anything. The running sync is simulated by holding
// its header-done channel open: the debounced call must not return
// until the channel closes.
func TestSyncFolderDebounceJoinsRunningSync(t *testing.T) {
	done := make(chan struct{})
	a := &App{
		ctx:             context.Background(),
		syncLastRequest: map[string]time.Time{"acc-1:folder-1": time.Now()},
		syncHeaderDone:  map[string]chan struct{}{"acc-1:folder-1": done},
	}

	returned := make(chan error, 1)
	go func() { returned <- a.SyncFolder("acc-1", "folder-1") }()

	select {
	case err := <-returned:
		t.Fatalf("debounced SyncFolder returned %v while the sync was still running (silent drop)", err)
	case <-time.After(100 * time.Millisecond):
		// Still waiting: it joined the running sync.
	}
	close(done)

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("joined SyncFolder = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced SyncFolder did not return after the running sync finished")
	}
}

// TestSyncFolderDebounceWithoutRunningSync: a debounce hit with no
// header-done channel (the previous sync already finished and cleaned
// up) still returns nil at once — there is nothing to join.
func TestSyncFolderDebounceWithoutRunningSync(t *testing.T) {
	a := &App{
		ctx:             context.Background(),
		syncLastRequest: map[string]time.Time{"acc-1:folder-1": time.Now()},
		syncHeaderDone:  map[string]chan struct{}{},
	}
	if err := a.SyncFolder("acc-1", "folder-1"); err != nil {
		t.Fatalf("SyncFolder = %v, want nil", err)
	}
}
