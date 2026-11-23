// Toast sender identity: the DisplayName and IconUri Windows shows for a
// toast's sender.
//
// go-toast's SetAppData writes DisplayName = the raw AppID, and worse, its
// writeStringValueImpl returns early without writing whenever the value
// already exists and is non-empty. Machines that ran v0.7.30/31 therefore
// already have DisplayName = "net.absons.daak" in
// HKCU\SOFTWARE\Classes\AppUserModelId\net.absons.daak, and SetAppData alone
// can never repair it. The code here writes the identity unconditionally
// through the registry API (SetStringValue overwrites), so a launch after
// upgrading repairs the stale value.
//
// The logic is platform-independent (it only depends on the store
// interface), so it is tested on any platform with a fake store seeded with
// the stale value; only opening the real key is Windows-only
// (notifier_windows.go).
package notification

import (
	"fmt"
	"os"
	"path/filepath"
)

// toastIdentityStore abstracts the AppUserModelId registry key. It is
// satisfied by registry.Key on Windows; tests use a fake.
type toastIdentityStore interface {
	SetStringValue(name, value string) error
	Close() error
}

// writeToastIdentity unconditionally writes DisplayName, and IconUri when
// iconPath is non-empty. There is deliberately no existence check here: the
// whole point is to overwrite a stale DisplayName left by earlier versions.
// An empty iconPath leaves IconUri untouched (e.g. a dev build with no
// shipped icon.ico) instead of clearing it.
func writeToastIdentity(store toastIdentityStore, displayName, iconPath string) error {
	defer store.Close()
	if err := store.SetStringValue("DisplayName", displayName); err != nil {
		return fmt.Errorf("setting DisplayName: %w", err)
	}
	if iconPath != "" {
		if err := store.SetStringValue("IconUri", iconPath); err != nil {
			return fmt.Errorf("setting IconUri: %w", err)
		}
	}
	return nil
}

// toastIconPathFor returns the absolute path of the icon.ico shipped next to
// the executable (the installer installs build/windows/icon.ico as
// $INSTDIR\icon.ico), or "" when it is absent — a dev build or an install
// from before v0.7.32. Deriving it from the running exe keeps it correct in
// both install scopes (per-user and all-users).
func toastIconPathFor(exePath string) string {
	if exePath == "" {
		return ""
	}
	p := filepath.Join(filepath.Dir(exePath), "icon.ico")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}
