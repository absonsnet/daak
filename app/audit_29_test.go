package app

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/platform"
	"github.com/absonsnet/daak/internal/settings"
)

// Audit finding 29 (v0.7.38 batch): SetAutostart persisted the setting
// before the OS effect ran, so a failed Enable/Disable (e.g. a Flatpak
// portal D-Bus failure) left the Settings toggle reporting a state
// that isn't real. The OS effect must run first; the stored value only
// changes after it succeeds. (SetRunBackground was already fixed the
// same way by finding 19 and is not re-done here.)

type failingAutostartManager struct {
	enableErr  error
	disableErr error
	enabled    bool
}

func (m *failingAutostartManager) Enable() error {
	if m.enableErr != nil {
		return m.enableErr
	}
	m.enabled = true
	return nil
}

func (m *failingAutostartManager) Disable() error {
	if m.disableErr != nil {
		return m.disableErr
	}
	m.enabled = false
	return nil
}

func (m *failingAutostartManager) IsEnabled() bool { return m.enabled }

var _ platform.AutostartManager = (*failingAutostartManager)(nil)

func newAutostartTestApp(t *testing.T, mgr platform.AutostartManager) *App {
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
		autostartMgr:  mgr,
	}
}

// A failed Enable leaves the stored setting off.
func TestSetAutostartFailedEnableLeavesStoredOff(t *testing.T) {
	mgr := &failingAutostartManager{enableErr: errors.New("portal D-Bus failure")}
	a := newAutostartTestApp(t, mgr)
	if err := a.SetAutostart(true); err == nil {
		t.Fatal("SetAutostart(true) with a failing Enable returned nil, want an error")
	}
	if on, _ := a.GetAutostart(); on {
		t.Fatal("autostart persisted as on after a failed Enable, want off")
	}
}

// A failed Disable leaves the stored setting on.
func TestSetAutostartFailedDisableLeavesStoredOn(t *testing.T) {
	mgr := &failingAutostartManager{disableErr: errors.New("portal D-Bus failure")}
	a := newAutostartTestApp(t, mgr)
	if err := a.settingsStore.SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetAutostart(false); err == nil {
		t.Fatal("SetAutostart(false) with a failing Disable returned nil, want an error")
	}
	if on, _ := a.GetAutostart(); !on {
		t.Fatal("autostart persisted as off after a failed Disable, want on")
	}
}

// The success path still persists after the OS effect runs.
func TestSetAutostartSuccessPersists(t *testing.T) {
	mgr := &failingAutostartManager{}
	a := newAutostartTestApp(t, mgr)
	if err := a.SetAutostart(true); err != nil {
		t.Fatalf("SetAutostart(true): %v", err)
	}
	if on, _ := a.GetAutostart(); !on {
		t.Fatal("autostart not persisted after a successful Enable, want on")
	}
	if !mgr.enabled {
		t.Fatal("Enable was not called on the OS manager")
	}
	if err := a.SetAutostart(false); err != nil {
		t.Fatalf("SetAutostart(false): %v", err)
	}
	if on, _ := a.GetAutostart(); on {
		t.Fatal("autostart still on after a successful Disable, want off")
	}
	if mgr.enabled {
		t.Fatal("Disable was not called on the OS manager")
	}
}

// A no-op (already at the requested value) does not call the OS
// manager at all, so a broken portal cannot fail a redundant toggle.
func TestSetAutostartNoOpSkipsOSCall(t *testing.T) {
	mgr := &failingAutostartManager{enableErr: errors.New("portal D-Bus failure")}
	a := newAutostartTestApp(t, mgr)
	if err := a.settingsStore.SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetAutostart(true); err != nil {
		t.Fatalf("SetAutostart(true) when already on returned %v, want nil (no OS call)", err)
	}
	if on, _ := a.GetAutostart(); !on {
		t.Fatal("autostart changed on a no-op set, want on")
	}
}
