package app

import (
	"path/filepath"
	"testing"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/settings"
)

// Audit finding 19 (v0.7.38 batch): closing the window must never
// strand the user with no window and no tray icon. Background mode
// requires a working tray, and the close paths hide only when the
// tray icon is actually up.

// newTrayGuardTestApp builds a minimal App with a real settings store
// and NO tray (nil), so tray start deterministically fails — the
// same state as a machine where the tray cannot start.
func newTrayGuardTestApp(t *testing.T) *App {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &App{
		db:            db,
		settingsStore: settings.NewStore(db),
		// tray intentionally nil: startTray() returns false.
	}
}

// Enabling background mode without a tray is refused, and the
// setting stays off — never a setting that promises background mode
// with no icon to bring the window back.
func TestSetRunBackgroundRefusesWithoutTray(t *testing.T) {
	a := newTrayGuardTestApp(t)
	if err := a.SetRunBackground(true); err == nil {
		t.Fatal("SetRunBackground(true) with no tray returned nil, want an error")
	}
	if on, _ := a.GetRunBackground(); on {
		t.Fatal("run_background persisted as on after a failed tray start, want off")
	}
}

// Enabling start-hidden without a tray is refused the same way, and
// neither setting is persisted.
func TestSetStartHiddenRefusesWithoutTray(t *testing.T) {
	a := newTrayGuardTestApp(t)
	if err := a.SetStartHidden(true); err == nil {
		t.Fatal("SetStartHidden(true) with no tray returned nil, want an error")
	}
	if on, _ := a.GetStartHidden(); on {
		t.Fatal("start_hidden persisted as on after a failed tray start, want off")
	}
	if on, _ := a.GetRunBackground(); on {
		t.Fatal("run_background persisted as on after a failed start-hidden, want off")
	}
}

// Disabling background mode still works with no tray, and still
// force-disables start_hidden.
func TestSetRunBackgroundDisableWorksWithoutTray(t *testing.T) {
	a := newTrayGuardTestApp(t)
	if err := a.settingsStore.SetRunBackground(true); err != nil {
		t.Fatal(err)
	}
	if err := a.settingsStore.SetStartHidden(true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRunBackground(false); err != nil {
		t.Fatalf("SetRunBackground(false): %v", err)
	}
	if on, _ := a.GetRunBackground(); on {
		t.Fatal("run_background still on after disable, want off")
	}
	if on, _ := a.GetStartHidden(); on {
		t.Fatal("start_hidden still on after disabling background mode, want off")
	}
}

// canHideOnClose: hide only when background mode is on AND the tray
// icon is actually up. Anything else quits for real.
func TestCanHideOnClose(t *testing.T) {
	a := newTrayGuardTestApp(t)

	// Background off: never hide, tray or no tray.
	a.trayStarted = true
	if a.canHideOnClose() {
		t.Fatal("canHideOnClose with background off returned true, want false")
	}

	// Background on, tray down: never hide.
	if err := a.settingsStore.SetRunBackground(true); err != nil {
		t.Fatal(err)
	}
	a.trayStarted = false
	if a.canHideOnClose() {
		t.Fatal("canHideOnClose with background on but no tray returned true, want false")
	}

	// Background on, tray up: hide.
	a.trayStarted = true
	if !a.canHideOnClose() {
		t.Fatal("canHideOnClose with background on and tray up returned false, want true")
	}
}
