package notification

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeIdentityStore is an in-memory stand-in for the AppUserModelId registry
// key, seeded with whatever a previous version left behind.
type fakeIdentityStore struct {
	values map[string]string
	closed bool
}

func (f *fakeIdentityStore) SetStringValue(name, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[name] = value
	return nil
}

func (f *fakeIdentityStore) Close() error {
	f.closed = true
	return nil
}

func TestWriteToastIdentityOverwritesStaleDisplayName(t *testing.T) {
	// Exact state left by v0.7.30/31 on a real machine: DisplayName is the
	// raw AppID, IconUri was never written. This is the trap go-toast's
	// SetAppData cannot escape (its write returns early when the value
	// exists); the direct write must overwrite it.
	store := &fakeIdentityStore{values: map[string]string{
		"DisplayName": "net.absons.daak",
	}}
	if err := writeToastIdentity(store, "Daak", `C:\Program Files\Daak\icon.ico`); err != nil {
		t.Fatalf("writeToastIdentity: %v", err)
	}
	if got := store.values["DisplayName"]; got != "Daak" {
		t.Errorf("DisplayName = %q, want %q (stale value was not overwritten)", got, "Daak")
	}
	if got := store.values["IconUri"]; got != `C:\Program Files\Daak\icon.ico` {
		t.Errorf("IconUri = %q, want the shipped icon path", got)
	}
	if !store.closed {
		t.Error("store was not closed")
	}
}

func TestWriteToastIdentityOverwritesStaleIconUri(t *testing.T) {
	store := &fakeIdentityStore{values: map[string]string{
		"DisplayName": "Daak",
		"IconUri":     `C:\Old\icon.ico`,
	}}
	if err := writeToastIdentity(store, "Daak", `C:\Program Files\Daak\icon.ico`); err != nil {
		t.Fatalf("writeToastIdentity: %v", err)
	}
	if got := store.values["IconUri"]; got != `C:\Program Files\Daak\icon.ico` {
		t.Errorf("IconUri = %q, want the new icon path", got)
	}
}

func TestWriteToastIdentityLeavesIconUriAloneWhenNoIcon(t *testing.T) {
	// No icon.ico shipped (dev build, or an install from before v0.7.32):
	// the name is still repaired, but IconUri is left as it was.
	store := &fakeIdentityStore{values: map[string]string{
		"DisplayName": "net.absons.daak",
		"IconUri":     `C:\Old\icon.ico`,
	}}
	if err := writeToastIdentity(store, "Daak", ""); err != nil {
		t.Fatalf("writeToastIdentity: %v", err)
	}
	if got := store.values["DisplayName"]; got != "Daak" {
		t.Errorf("DisplayName = %q, want %q", got, "Daak")
	}
	if got := store.values["IconUri"]; got != `C:\Old\icon.ico` {
		t.Errorf("IconUri = %q, want it untouched when no icon is shipped", got)
	}
}

func TestToastIconPathFor(t *testing.T) {
	dir := t.TempDir()
	if got := toastIconPathFor(filepath.Join(dir, "daak.exe")); got != "" {
		t.Errorf("toastIconPathFor without icon.ico = %q, want empty", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "icon.ico"), []byte("fake ico"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "icon.ico")
	if got := toastIconPathFor(filepath.Join(dir, "daak.exe")); got != want {
		t.Errorf("toastIconPathFor with icon.ico = %q, want %q", got, want)
	}
	if got := toastIconPathFor(""); got != "" {
		t.Errorf("toastIconPathFor(%q) = %q, want empty", "", got)
	}
}
