package notification

// PermissionStatus describes whether the OS will deliver notifications
// from this app. A denied permission used to fail silently forever:
// the app asked once, the user said no, and every later notification
// was dropped with no record anywhere. The status is surfaced in
// Settings and drives a one-time in-app notice so the user can find
// out why new-mail notifications never arrive.
type PermissionStatus string

const (
	// PermissionAuthorized means the OS will deliver notifications.
	PermissionAuthorized PermissionStatus = "authorized"
	// PermissionDenied means the user (or the OS) blocked notifications
	// for this app. Re-enabling requires the system settings page.
	PermissionDenied PermissionStatus = "denied"
	// PermissionNotDetermined means the OS has not asked the user yet.
	PermissionNotDetermined PermissionStatus = "not-determined"
	// PermissionUnknown means the permission could not be determined
	// (query failed, timed out, or the platform exposes no per-app
	// permission query). It is NOT a denied state.
	PermissionUnknown PermissionStatus = "unknown"
)

// darwinStatus codes are the integers returned by
// getNotificationPermissionStatus in notifier_darwin.m — keep the two
// sides in sync.
const (
	darwinStatusAuthorized    = 0
	darwinStatusDenied        = 1
	darwinStatusNotDetermined = 2
	darwinStatusUnknown       = 3
)

// OpenSystemNotificationSettings opens the OS page where the user can
// re-enable notifications for this app (macOS System Settings, deep-linked
// to this app's own row). On platforms without such a page it returns an
// error; callers should only offer it where the platform supports it.
func OpenSystemNotificationSettings() error {
	return openSystemNotificationSettings()
}

// darwinAuthorizationStatusToPermission maps a darwin status code to a
// PermissionStatus. Any code outside the known set maps to
// PermissionUnknown rather than guessing — an unmapped value must never
// be reported as denied.
func darwinAuthorizationStatusToPermission(code int) PermissionStatus {
	switch code {
	case darwinStatusAuthorized:
		return PermissionAuthorized
	case darwinStatusDenied:
		return PermissionDenied
	case darwinStatusNotDetermined:
		return PermissionNotDetermined
	default:
		return PermissionUnknown
	}
}
