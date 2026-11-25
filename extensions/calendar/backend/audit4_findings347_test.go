package backend

import (
	"strings"
	"testing"
	"time"
)

// Fourth-audit findings 3, 4, 7: instance identity must be the ORIGINAL
// occurrence start — never the edited time, and never the master's first
// occurrence — and whole-series edits must keep overrides that still map
// onto the new occurrence set.

func TestResolveInstanceUnix(t *testing.T) {
	orig := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC).Unix()
	moved := time.Date(2026, 6, 12, 16, 0, 0, 0, time.UTC).Unix()
	in := EventUpdateInput{
		OriginalInstanceTimeUnix: orig,
		EventInput:               EventInput{DTStartUnix: moved},
	}
	if got := resolveInstanceUnix(in); got != orig {
		t.Errorf("resolveInstanceUnix with original set = %d, want %d (the edited start %d must not be the identity)", got, orig, moved)
	}
	in.OriginalInstanceTimeUnix = 0
	if got := resolveInstanceUnix(in); got != moved {
		t.Errorf("resolveInstanceUnix without original = %d, want fallback %d", got, moved)
	}
}

func TestSerializeVEVENTWithRecurrenceID_UsesOriginalTime(t *testing.T) {
	orig := time.Date(2026, 6, 12, 14, 0, 0, 0, time.UTC).Unix()
	moved := time.Date(2026, 6, 12, 16, 30, 0, 0, time.UTC).Unix()
	blob, err := serializeVEVENTWithRecurrenceID("uid@daak-test", EventInput{
		CalendarID:  "cal1",
		Summary:     "Moved",
		DTStartUnix: moved,
		DTEndUnix:   moved + 3600,
	}, orig)
	if err != nil {
		t.Fatalf("serializeVEVENTWithRecurrenceID: %v", err)
	}
	// RECURRENCE-ID must be the ORIGINAL occurrence start...
	if !strings.Contains(blob, "RECURRENCE-ID:"+formatICSDateTime(time.Unix(orig, 0))) {
		t.Errorf("blob RECURRENCE-ID is not the original occurrence time:\n%s", blob)
	}
	// ...while DTSTART carries the NEW start.
	if !strings.Contains(blob, "DTSTART:"+formatICSDateTime(time.Unix(moved, 0))) {
		t.Errorf("blob DTSTART is not the new start:\n%s", blob)
	}
}

func dailyEvent(t *testing.T, start time.Time) Event {
	t.Helper()
	blob, err := serializeVEVENT("uid@daak-test", EventInput{
		CalendarID:  "cal1",
		Summary:     "Daily",
		DTStartUnix: start.Unix(),
		DTEndUnix:   start.Add(time.Hour).Unix(),
		Recurrence:  &RecurrenceSpec{Freq: "DAILY"},
	})
	if err != nil {
		t.Fatalf("serializeVEVENT: %v", err)
	}
	return Event{
		ID:          "ev1",
		DTStartUnix: start.Unix(),
		DTEndUnix:   start.Add(time.Hour).Unix(),
		RRuleText:   "FREQ=DAILY",
		ICSBlob:     blob,
	}
}

func TestRetainedOverrides_KeepMappedDropUnmapped(t *testing.T) {
	start := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	ev := dailyEvent(t, start)
	day := 24 * time.Hour
	overrides := []EventOverride{
		{EventID: "ev1", RecurrenceIDUnix: start.Add(day).Unix()},               // Jun 6 14:00 — maps
		{EventID: "ev1", RecurrenceIDUnix: start.Add(2 * day).Unix()},           // Jun 7 14:00 — maps
		{EventID: "ev1", RecurrenceIDUnix: start.Add(-5 * time.Hour).Unix()},    // Jun 5 09:00 — before series start
		{EventID: "ev1", RecurrenceIDUnix: start.Add(3*day + time.Hour).Unix()}, // Jun 8 15:00 — wrong time of day
	}
	kept, ok := retainedOverrides(ev, overrides)
	if !ok {
		t.Fatal("retainedOverrides reported failure")
	}
	if len(kept) != 2 {
		t.Fatalf("retainedOverrides kept %d, want 2: %+v", len(kept), kept)
	}
	for _, k := range kept {
		if k.RecurrenceIDUnix != start.Add(day).Unix() && k.RecurrenceIDUnix != start.Add(2*day).Unix() {
			t.Errorf("unexpected kept override: %+v", k)
		}
	}
}

func TestRetainedOverrides_NonRecurring(t *testing.T) {
	start := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	blob, err := serializeVEVENT("uid@daak-test", EventInput{
		CalendarID:  "cal1",
		Summary:     "Once",
		DTStartUnix: start.Unix(),
		DTEndUnix:   start.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("serializeVEVENT: %v", err)
	}
	ev := Event{ID: "ev1", DTStartUnix: start.Unix(), DTEndUnix: start.Add(time.Hour).Unix(), ICSBlob: blob}
	overrides := []EventOverride{
		{EventID: "ev1", RecurrenceIDUnix: start.Unix()},                     // the single instance — maps
		{EventID: "ev1", RecurrenceIDUnix: start.Add(24 * time.Hour).Unix()}, // no such occurrence
	}
	kept, ok := retainedOverrides(ev, overrides)
	if !ok {
		t.Fatal("retainedOverrides reported failure")
	}
	if len(kept) != 1 || kept[0].RecurrenceIDUnix != start.Unix() {
		t.Fatalf("retainedOverrides kept %+v, want only the single instance", kept)
	}
}

func TestMergeOverrideBlobs(t *testing.T) {
	start := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	ev := dailyEvent(t, start)
	ovBlob, err := serializeVEVENTWithRecurrenceID("uid@daak-test", EventInput{
		CalendarID:  "cal1",
		Summary:     "Moved instance",
		DTStartUnix: start.Add(24 * time.Hour).Add(2 * time.Hour).Unix(),
		DTEndUnix:   start.Add(24 * time.Hour).Add(3 * time.Hour).Unix(),
	}, start.Add(24*time.Hour).Unix())
	if err != nil {
		t.Fatalf("serializeVEVENTWithRecurrenceID: %v", err)
	}
	merged, err := mergeOverrideBlobs(ev.ICSBlob, []EventOverride{
		{EventID: "ev1", RecurrenceIDUnix: start.Add(24 * time.Hour).Unix(), ICSBlob: ovBlob},
	})
	if err != nil {
		t.Fatalf("mergeOverrideBlobs: %v", err)
	}
	if strings.Count(merged, "BEGIN:VEVENT") != 2 {
		t.Errorf("merged blob has %d VEVENTs, want 2:\n%s", strings.Count(merged, "BEGIN:VEVENT"), merged)
	}
	if !strings.Contains(merged, "RECURRENCE-ID:") {
		t.Errorf("merged blob lost the override RECURRENCE-ID:\n%s", merged)
	}
}
