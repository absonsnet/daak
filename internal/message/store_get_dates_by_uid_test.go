package message

import (
	"testing"
	"time"
)

// TestGetDatesByUID supports the re-audit finding R2 guard: the period
// sync's delete loop needs each candidate UID's stored header Date in one
// query to tell a real expunge from old mail the SINCE listing never
// covered.
func TestGetDatesByUID(t *testing.T) {
	s, _, accountID, folderID := newBodyFailedTestStore(t)
	now := time.Now().UTC()

	old := &Message{ID: "old", AccountID: accountID, FolderID: folderID, UID: 1, Date: now.AddDate(0, 0, -90)}
	recent := &Message{ID: "recent", AccountID: accountID, FolderID: folderID, UID: 2, Date: now.AddDate(0, 0, -2)}
	dateless := &Message{ID: "dateless", AccountID: accountID, FolderID: folderID, UID: 3}
	for _, m := range []*Message{old, recent, dateless} {
		if err := s.Create(m); err != nil {
			t.Fatalf("Create %s: %v", m.ID, err)
		}
	}

	dates, err := s.GetDatesByUID(folderID, []uint32{1, 2, 3, 99})
	if err != nil {
		t.Fatalf("GetDatesByUID: %v", err)
	}
	if got := dates[1]; got.IsZero() || got.Year() != old.Date.Year() || got.YearDay() != old.Date.YearDay() {
		t.Errorf("dates[1] = %v, want the stored old date %v", got, old.Date)
	}
	if got := dates[2]; got.IsZero() || got.YearDay() != recent.Date.YearDay() {
		t.Errorf("dates[2] = %v, want the stored recent date %v", got, recent.Date)
	}
	// A stored zero date round-trips as zero: the R2 guard treats it as
	// "cannot prove in-window" and keeps the row.
	if got, ok := dates[3]; !ok || !got.IsZero() {
		t.Errorf("dates[3] = %v (present=%v), want zero time", got, ok)
	}
	// A UID with no row at all is absent, not zero-valued in the map.
	if _, ok := dates[99]; ok {
		t.Errorf("dates[99] present, want absent (no such row)")
	}

	// Empty input is a no-op, not an error (no IN () query).
	empty, err := s.GetDatesByUID(folderID, nil)
	if err != nil {
		t.Fatalf("GetDatesByUID(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("GetDatesByUID(nil) = %v, want empty map", empty)
	}
}
