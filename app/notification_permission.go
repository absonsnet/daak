package app

import (
	"runtime"

	"github.com/absonsnet/daak/internal/logging"
	"github.com/absonsnet/daak/internal/notification"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// Notification permission status (re-audit finding R28)
// ============================================================================
//
// A denied macOS notification permission used to fail silently forever:
// the app asked once, the user said no, and every later notification was
// dropped with no record anywhere. The permission state is now queryable
// (NotificationPermissionInfo), surfaced in Settings, and a denial is
// announced once in-app so the user can re-enable notifications.

// NotificationPermissionInfo is the Wails-bound view of the OS
// notification permission state.
type NotificationPermissionInfo struct {
	// Status is one of "authorized", "denied", "not-determined" or
	// "unknown". "unknown" means the permission could not be
	// determined (or the platform exposes no per-app query) — it is
	// NOT a denied state.
	Status string `json:"status"`
	// Platform is runtime.GOOS ("darwin", "windows", "linux").
	Platform string `json:"platform"`
	// CanOpenSystemSettings is true only on macOS, where Daak can
	// deep-link to the Notifications settings pane. The frontend
	// must not show a macOS-only settings link on other platforms.
	CanOpenSystemSettings bool `json:"canOpenSystemSettings"`
}

// GetNotificationPermissionStatus returns the current OS notification
// permission state. Pure query — it never claims the one-time denied
// notice (see CheckNotificationDeniedNotice), so opening Settings
// cannot silently consume the notice.
func (a *App) GetNotificationPermissionStatus() (NotificationPermissionInfo, error) {
	status := notification.PermissionUnknown
	if a.notifier != nil {
		status = a.notifier.PermissionStatus()
	}
	return NotificationPermissionInfo{
		Status:                string(status),
		Platform:              runtime.GOOS,
		CanOpenSystemSettings: runtime.GOOS == "darwin",
	}, nil
}

// OpenNotificationSettings opens the OS page where the user can
// re-enable notifications for this app (macOS System Settings,
// deep-linked to Daak's own row). Returns an error on platforms
// without such a page; the frontend only offers it on macOS.
func (a *App) OpenNotificationSettings() error {
	return notification.OpenSystemNotificationSettings()
}

// CheckNotificationDeniedNotice reports whether the one-time
// "notifications are blocked" notice should be shown now. It is a
// PEEK, not a claim (fourth-audit finding 54): it returns true every
// time a denied permission is observed while the notice flag is
// unset, and only MarkNotificationDeniedNoticeShown persists the
// flag — after the dialog has actually opened. Claiming here used
// to mark the notice "already shown" the moment it was claimed, so
// if the app closed before the dialog appeared, the notice never
// came again.
func (a *App) CheckNotificationDeniedNotice() (bool, error) {
	return a.deniedNoticePending()
}

// MarkNotificationDeniedNoticeShown records that the one-time
// "notifications are blocked" notice has actually been displayed.
// The frontend calls it when the dialog opens — never before — so a
// notice claimed but never shown (app closed first, another dialog
// in the way) still fires on the next launch.
func (a *App) MarkNotificationDeniedNoticeShown() error {
	if a.settingsStore == nil {
		return nil
	}
	return a.settingsStore.SetNotificationDeniedNoticeShown(true)
}

// deniedNoticePending is the single read behind the one-time denied
// notice: true only when the OS reports denied AND the flag is
// unset. It never writes.
func (a *App) deniedNoticePending() (bool, error) {
	if a.notifier == nil || a.settingsStore == nil {
		return false, nil
	}
	if a.notifier.PermissionStatus() != notification.PermissionDenied {
		return false, nil
	}
	shown, err := a.settingsStore.GetNotificationDeniedNoticeShown()
	if err != nil {
		return false, err
	}
	return !shown, nil
}

// handleNotificationPermissionChange is invoked by the notifier when
// the OS reports a permission change (macOS: the user answered the
// authorization prompt). A denial emits the
// "notification:permission-denied" event; the frontend peeks at the
// one-time notice via CheckNotificationDeniedNotice and marks it
// shown when the dialog opens, so an event emitted before the
// frontend mounts is not lost — the startup check still queues it
// then. Non-denied changes are ignored here: the Settings row shows
// the current state on its next load.
func (a *App) handleNotificationPermissionChange(status notification.PermissionStatus) {
	log := logging.WithComponent("app.notify")
	if status != notification.PermissionDenied {
		return
	}
	log.Warn().Msg("Notification permission denied — new-mail notifications will be dropped until re-enabled in System Settings")
	if a.ctx != nil {
		wailsRuntime.EventsEmit(a.ctx, "notification:permission-denied", map[string]interface{}{})
	}
}
