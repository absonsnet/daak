//go:build !darwin

package notification

import "testing"

// Re-audit finding R28: opening the system notification settings page
// is a macOS-only affordance — other platforms must fail loudly
// rather than silently doing nothing.
func TestOpenSystemNotificationSettingsUnsupported(t *testing.T) {
	if err := OpenSystemNotificationSettings(); err == nil {
		t.Fatal("OpenSystemNotificationSettings() on non-macOS returned nil, want an error")
	}
}

// Non-macOS notifiers expose no per-app permission query, so they
// report unknown — never denied.
func TestNonDarwinNotifierPermissionStatusIsUnknown(t *testing.T) {
	n := New("Daak", false)
	if got := n.PermissionStatus(); got != PermissionUnknown {
		t.Errorf("PermissionStatus() = %q, want %q", got, PermissionUnknown)
	}
	// Registering a permission-change handler must not panic; non-macOS
	// platforms simply have no OS signal to invoke it with.
	n.SetPermissionChangeHandler(func(PermissionStatus) {
		t.Error("permission change handler invoked on non-macOS, want never invoked")
	})
}
