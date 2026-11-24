package sync

import (
	"testing"
	"time"
)

// TestEffectiveHeaderDate pins the audit finding-6 header fallback: a message
// with no usable Date header stores the IMAP internal date (arrival time)
// instead of a zero date. A zero date used to be purged by the sync-period
// DeleteOlderThan on every cycle, so the message was re-fetched forever.
func TestEffectiveHeaderDate(t *testing.T) {
	arrival := time.Date(2026, 11, 20, 10, 30, 0, 0, time.FixedZone("CET", 3600))
	headerDate := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)

	// No header Date: fall back to the internal (arrival) date, in UTC.
	if got := effectiveHeaderDate(time.Time{}, arrival); !got.Equal(arrival.UTC()) {
		t.Fatalf("zero header date: got %v, want %v", got, arrival.UTC())
	}
	// A real header Date wins over the internal date.
	if got := effectiveHeaderDate(headerDate, arrival); !got.Equal(headerDate) {
		t.Fatalf("header date present: got %v, want %v", got, headerDate)
	}
	// Neither available: stays zero (nothing to fall back to).
	if got := effectiveHeaderDate(time.Time{}, time.Time{}); !got.IsZero() {
		t.Fatalf("no dates at all: got %v, want zero", got)
	}
	// Header Date present, no internal date: header stands.
	if got := effectiveHeaderDate(headerDate, time.Time{}); !got.Equal(headerDate) {
		t.Fatalf("header date, no internal date: got %v, want %v", got, headerDate)
	}
}
