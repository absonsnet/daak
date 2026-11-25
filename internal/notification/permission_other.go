//go:build !darwin

package notification

import "errors"

// openSystemNotificationSettings is the non-macOS implementation: no
// platform here exposes a per-app notification settings page that Daak
// can open, so callers must hide the "open settings" affordance.
func openSystemNotificationSettings() error {
	return errors.New("opening the system notification settings page is only supported on macOS")
}
