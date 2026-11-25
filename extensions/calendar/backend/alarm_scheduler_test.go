package backend

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	coreapi "github.com/absonsnet/daak/internal/core/api/v1"
)

// fakeNotifier records Show calls and fails the first failTimes calls.
type fakeNotifier struct {
	mu        sync.Mutex
	failTimes int
	calls     int
	shown     []string
}

func (f *fakeNotifier) Show(req coreapi.NotifyRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.shown = append(f.shown, req.Title)
	if f.calls <= f.failTimes {
		return errors.New("simulated notification failure")
	}
	return nil
}

func (f *fakeNotifier) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func seedAlarmEvent(t *testing.T, store *Store, eventID string, startUnix int64) {
	t.Helper()
	if err := store.WithTx(func(tx *sql.Tx) error {
		if err := store.CreateSourceTx(tx, Source{ID: "src-1", Type: SourceTypeLocal, Name: "Local", Enabled: true, Writable: true, CreatedAt: startUnix}); err != nil {
			return err
		}
		if err := store.CreateCalendarTx(tx, Calendar{ID: "cal-1", SourceID: "src-1", DisplayName: "Cal", URL: "local", CreatedAt: startUnix}); err != nil {
			return err
		}
		return store.UpsertEventTx(tx, Event{ID: eventID, CalendarID: "cal-1", UID: "u-" + eventID, Summary: "Test event", DTStartUnix: startUnix, DTEndUnix: startUnix + 3600})
	}); err != nil {
		t.Fatal(err)
	}
}

func seedPendingAlarm(t *testing.T, store *Store, id, eventID string, triggerUnix int64) {
	t.Helper()
	if err := store.WithTx(func(tx *sql.Tx) error {
		return store.UpsertAlarmTx(tx, Alarm{ID: id, EventID: eventID, InstanceUnix: triggerUnix + 900, TriggerUnix: triggerUnix, Status: "pending", Action: "display"})
	}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSchedulerDispatch_RetriesFailedNotification (fourth-audit finding
// 32): a notification that fails to show must NOT mark the alarm fired —
// it stays pending and retries until it shows.
func TestSchedulerDispatch_RetriesFailedNotification(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	seedAlarmEvent(t, store, "ev-1", now)
	seedPendingAlarm(t, store, "a-1", "ev-1", now)

	notif := &fakeNotifier{failTimes: 1}
	s := NewAlarmScheduler(store, notif, nil, nil)
	s.retryDelay = 50 * time.Millisecond

	s.dispatch("a-1")

	// After the failed Show the alarm is still pending, not fired.
	if a, err := store.GetAlarm("a-1"); err != nil || a == nil || a.Status != "pending" {
		t.Fatalf("alarm after failed notification = %+v, %v; want pending", a, err)
	}
	// The retry fires and the second Show succeeds → fired.
	waitFor(t, 2*time.Second, "alarm to fire after retry", func() bool {
		a, _ := store.GetAlarm("a-1")
		return a != nil && a.Status == "fired"
	})
	if got := notif.Calls(); got != 2 {
		t.Errorf("notification calls = %d, want 2 (failed once, retried once)", got)
	}
}

// TestSchedulerDispatch_GivesUpAfterMaxRetries: a notifier that never
// succeeds still ends in a fired alarm after the retry budget, instead
// of retrying forever.
func TestSchedulerDispatch_GivesUpAfterMaxRetries(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	seedAlarmEvent(t, store, "ev-1", now)
	seedPendingAlarm(t, store, "a-1", "ev-1", now)

	notif := &fakeNotifier{failTimes: 100}
	s := NewAlarmScheduler(store, notif, nil, nil)
	s.retryDelay = 20 * time.Millisecond

	s.dispatch("a-1")
	waitFor(t, 5*time.Second, "alarm to give up and fire", func() bool {
		a, _ := store.GetAlarm("a-1")
		return a != nil && a.Status == "fired"
	})
	if got := notif.Calls(); got != alarmMaxRetries+1 {
		t.Errorf("notification calls = %d, want %d (initial + %d retries)", got, alarmMaxRetries+1, alarmMaxRetries)
	}
}

// TestSchedulerPeriodicTickArmsLateAlarms (fourth-audit finding 33): an
// alarm inserted after Start — with no sync event at all, as with a
// local-calendar event — is armed by the periodic tick and fires.
func TestSchedulerPeriodicTickArmsLateAlarms(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().Unix()
	seedAlarmEvent(t, store, "ev-1", now)

	notif := &fakeNotifier{}
	s := NewAlarmScheduler(store, notif, nil, nil)
	s.reevaluateInterval = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	// Insert the alarm after the scheduler started; nothing else
	// (no sync-complete, no wake) will ever tell it about the row.
	seedPendingAlarm(t, store, "a-1", "ev-1", now+1)

	waitFor(t, 5*time.Second, "alarm to fire via periodic tick", func() bool {
		a, _ := store.GetAlarm("a-1")
		return a != nil && a.Status == "fired"
	})
	if got := notif.Calls(); got != 1 {
		t.Errorf("notification calls = %d, want 1", got)
	}
}
