package app

import (
	"testing"

	"github.com/absonsnet/daak/internal/tray"
)

// Re-audit finding R18 (v0.7.40 batch): trayStarted is set once at
// startup and never cleared, so a tray that dies mid-session still
// claims to be up — and closing the window hides it with no tray icon
// to bring it back. canHideOnClose (and GetStartHiddenActive) must
// re-check tray liveness at close time; a dead tray falls back to a
// real quit (or a visible window), never a hide.

// canHideOnClose with a dead tray: the stale started flag on its own
// would hide the window. A real tray that never started stands in
// for the dead one — Alive() is false either way, which is the state
// the tray's Done channel leaves behind once its run loop exits.
func TestCanHideOnCloseDeadTray(t *testing.T) {
	a := newTrayGuardTestApp(t)
	a.tray = tray.New()
	a.trayStarted = true // stale flag, as after a mid-session tray death
	if err := a.settingsStore.SetRunBackground(true); err != nil {
		t.Fatal(err)
	}
	if a.canHideOnClose() {
		t.Fatal("canHideOnClose with a dead tray returned true, want false: hiding would strand the window with no tray icon to bring it back")
	}
}

// GetStartHiddenActive with a dead tray: starting hidden would strand
// the window the same way, so it must report inactive.
func TestGetStartHiddenActiveDeadTray(t *testing.T) {
	a := newTrayGuardTestApp(t)
	a.tray = tray.New()
	a.trayStarted = true // stale flag, as after a mid-session tray death
	if err := a.settingsStore.SetRunBackground(true); err != nil {
		t.Fatal(err)
	}
	if err := a.settingsStore.SetStartHidden(true); err != nil {
		t.Fatal(err)
	}
	if a.GetStartHiddenActive() {
		t.Fatal("GetStartHiddenActive with a dead tray returned true, want false: starting hidden would strand the window with no tray icon to bring it back")
	}
}
