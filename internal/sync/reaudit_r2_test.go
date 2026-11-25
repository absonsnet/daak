package sync

import (
	"testing"
	"time"
)

// Re-audit finding R2: with a sync period the remote UID listing comes
// from UID SEARCH SINCE <cutoff>, so it only covers mail inside the
// window. reconcileUIDs treats any local UID missing from that listing as
// expunged, and the empty-remote / >50% guards only cover the 0% and
// majority cases. A local row dated before the window but still on the
// server — old mail opened from server search, or moved in by another
// client — was therefore deleted as an ordinary expunge, re-fetched as
// new on the next sync, and deleted again. filterPeriodDeletableUIDs is
// the guard: the delete loop skips rows dated before the cutoff, leaving
// them to DeleteOlderThan (purged by received_at).

func TestFilterPeriodDeletableUIDs_NoPeriodKeepsAll(t *testing.T) {
	// No sync period (zero sinceDate): the remote listing is unfiltered,
	// so every missing UID is a real expunge.
	keep, skipped := filterPeriodDeletableUIDs(
		[]uint32{1, 2, 3},
		map[uint32]time.Time{1: time.Now().AddDate(0, 0, -100)},
		time.Time{},
	)
	if len(keep) != 3 || len(skipped) != 0 {
		t.Fatalf("keep=%v skipped=%v, want keep=[1 2 3] skipped=[]", keep, skipped)
	}
}

func TestFilterPeriodDeletableUIDs_OldMessageSkipped(t *testing.T) {
	// The brief's scenario: UID 5 is a 90-day-old message opened from
	// server search (still on the server, just outside the 30-day
	// window); UIDs 6 and 7 are recent mail genuinely expunged.
	since := time.Now().AddDate(0, 0, -30)
	dates := map[uint32]time.Time{
		5: time.Now().AddDate(0, 0, -90), // old, still on server
		6: time.Now().AddDate(0, 0, -2),  // in window, expunged
		7: time.Now().AddDate(0, 0, -1),  // in window, expunged
	}
	keep, skipped := filterPeriodDeletableUIDs([]uint32{5, 6, 7}, dates, since)
	if len(keep) != 2 || keep[0] != 6 || keep[1] != 7 {
		t.Fatalf("keep=%v, want [6 7] (only in-window expunges delete)", keep)
	}
	if len(skipped) != 1 || skipped[0] != 5 {
		t.Fatalf("skipped=%v, want [5] (old-but-on-server survives)", skipped)
	}
}

func TestFilterPeriodDeletableUIDs_BoundaryAndUnknownDates(t *testing.T) {
	since := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	dates := map[uint32]time.Time{
		1: since,                   // exactly at the cutoff: in window, deletes
		2: since.Add(-time.Second), // one second before: skipped
		3: time.Time{},             // zero date: cannot prove in-window, skipped
		// UID 4 absent from the map: no stored row/date, skipped
	}
	keep, skipped := filterPeriodDeletableUIDs([]uint32{1, 2, 3, 4}, dates, since)
	if len(keep) != 1 || keep[0] != 1 {
		t.Fatalf("keep=%v, want [1]", keep)
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped=%v, want [2 3 4]", skipped)
	}
}

func TestFilterPeriodDeletableUIDs_Empty(t *testing.T) {
	keep, skipped := filterPeriodDeletableUIDs(nil, nil, time.Now())
	if len(keep) != 0 || len(skipped) != 0 {
		t.Fatalf("keep=%v skipped=%v, want both empty", keep, skipped)
	}
}
