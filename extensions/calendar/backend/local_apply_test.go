package backend

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// Audit4 findings 20 + 28: the shared local-apply functions for scoped
// instance edits — used by the API after a push, by updateInstance to
// soft-commit an offline edit, and by Drain after a successful replay.

func seedRecurringMaster(t *testing.T, store *Store, calID string) (Event, int64, int64) {
	t.Helper()
	start := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC).Unix() // Friday
	blob, err := serializeVEVENT("rec@daak", EventInput{
		CalendarID:  calID,
		Summary:     "Standup",
		DTStartUnix: start,
		DTEndUnix:   start + 3600,
		Recurrence:  &RecurrenceSpec{Freq: "WEEKLY", Count: 4},
	})
	if err != nil {
		t.Fatalf("serializeVEVENT: %v", err)
	}
	master := Event{
		ID: "evt-rec-1", CalendarID: calID, UID: "rec@daak",
		Summary: "Standup", DTStartUnix: start, DTEndUnix: start + 3600,
		RRuleText: "FREQ=WEEKLY;COUNT=4", ICSBlob: blob,
	}
	if err := store.WithTx(func(tx *sql.Tx) error {
		return store.UpsertEventTx(tx, master)
	}); err != nil {
		t.Fatalf("UpsertEventTx: %v", err)
	}
	// Weekly occurrences: Jun 5, 12, 19, 26.
	return master, start + 7*24*3600, start + 14*24*3600
}

func TestApplyThisDeleteLocal(t *testing.T) {
	store := newTestStore(t)
	_, calID := seedGoogleSource(t, store, "primary")
	master, inst2, _ := seedRecurringMaster(t, store, calID)
	// An existing override for that occurrence (a moved instance).
	overrideBlob, err := serializeVEVENTWithRecurrenceID(master.UID, EventInput{
		CalendarID: calID, Summary: "Standup (moved)",
		DTStartUnix: inst2 + 3600, DTEndUnix: inst2 + 7200,
	}, inst2)
	if err != nil {
		t.Fatalf("serialize override: %v", err)
	}
	if err := store.WithTx(func(tx *sql.Tx) error {
		return store.UpsertOverrideTx(tx, master.ID, inst2, overrideBlob)
	}); err != nil {
		t.Fatalf("UpsertOverrideTx: %v", err)
	}

	if err := applyThisDeleteLocal(store, master, inst2, PushInstanceResult{}); err != nil {
		t.Fatalf("applyThisDeleteLocal: %v", err)
	}
	got, err := store.GetEvent(master.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !strings.Contains(got.ICSBlob, "EXDATE") {
		t.Fatalf("master blob after delete has no EXDATE:\n%s", got.ICSBlob)
	}
	overrides, err := store.ListOverrides(master.ID)
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(overrides) != 0 {
		t.Fatalf("overrides after delete = %d, want 0", len(overrides))
	}
}

func TestApplyThisUpdateLocal(t *testing.T) {
	store := newTestStore(t)
	_, calID := seedGoogleSource(t, store, "primary")
	master, inst2, _ := seedRecurringMaster(t, store, calID)

	in := EventInput{
		CalendarID: calID, Summary: "Standup (rescheduled)",
		DTStartUnix: inst2 + 7200, DTEndUnix: inst2 + 10800,
	}
	if err := applyThisUpdateLocal(store, master, in, inst2, PushInstanceResult{}); err != nil {
		t.Fatalf("applyThisUpdateLocal: %v", err)
	}
	overrides, err := store.ListOverrides(master.ID)
	if err != nil {
		t.Fatalf("ListOverrides: %v", err)
	}
	if len(overrides) != 1 || overrides[0].RecurrenceIDUnix != inst2 {
		t.Fatalf("overrides = %+v, want one for instance %d", overrides, inst2)
	}
	if !strings.Contains(overrides[0].ICSBlob, "Standup (rescheduled)") {
		t.Fatalf("override blob missing new summary:\n%s", overrides[0].ICSBlob)
	}
}

func TestApplyThisAndFutureDeleteLocal(t *testing.T) {
	store := newTestStore(t)
	_, calID := seedGoogleSource(t, store, "primary")
	master, _, inst3 := seedRecurringMaster(t, store, calID)

	if err := applyThisAndFutureDeleteLocal(store, master, inst3, PushInstanceResult{}); err != nil {
		t.Fatalf("applyThisAndFutureDeleteLocal: %v", err)
	}
	got, err := store.GetEvent(master.ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !strings.Contains(got.RRuleText, "UNTIL=") {
		t.Fatalf("master RRULE after clamp = %q, want an UNTIL", got.RRuleText)
	}
	if strings.Contains(got.RRuleText, "COUNT=4") {
		t.Fatalf("master RRULE after clamp = %q, want COUNT replaced by UNTIL", got.RRuleText)
	}
}
