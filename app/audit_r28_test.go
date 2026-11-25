package app

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/absonsnet/daak/internal/database"
	"github.com/absonsnet/daak/internal/notification"
	"github.com/absonsnet/daak/internal/settings"
)

// Re-audit finding R28: a denied macOS notification permission failed
// silently forever — no record, no warning, no guidance. These tests
// cover the Go-side reporting path: a denied permission is reported
// as denied (not silently disabled), and the one-time notice fires
// exactly once.

// fakeR28Notifier is a controllable stand-in for notification.Notifier.
type fakeR28Notifier struct {
	status            notification.PermissionStatus
	permissionHandler func(notification.PermissionStatus)
}

func (f *fakeR28Notifier) Start(ctx context.Context) error { return nil }
func (f *fakeR28Notifier) Stop()                           {}
func (f *fakeR28Notifier) Show(n notification.Notification) (uint32, error) {
	return 0, nil
}
func (f *fakeR28Notifier) SetClickHandler(h notification.ClickHandler) {}
func (f *fakeR28Notifier) PermissionStatus() notification.PermissionStatus {
	return f.status
}
func (f *fakeR28Notifier) SetPermissionChangeHandler(h func(notification.PermissionStatus)) {
	f.permissionHandler = h
}

// newR28TestApp builds a minimal App with a real settings store and the
// given fake notifier. ctx stays nil (as in the other audit tests), so
// no Wails event is ever emitted here.
func newR28TestApp(t *testing.T, notifier notification.Notifier) *App {
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
		notifier:      notifier,
	}
}

// A denied permission is reported as denied — the Settings row and the
// notice both key off this, so "unknown" or a silent drop here would
// reproduce the original bug.
func TestR28DeniedPermissionReportedAsDenied(t *testing.T) {
	a := newR28TestApp(t, &fakeR28Notifier{status: notification.PermissionDenied})

	info, err := a.GetNotificationPermissionStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != string(notification.PermissionDenied) {
		t.Errorf("Status = %q, want %q", info.Status, notification.PermissionDenied)
	}
	if info.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", info.Platform, runtime.GOOS)
	}
	if info.CanOpenSystemSettings != (runtime.GOOS == "darwin") {
		t.Errorf("CanOpenSystemSettings = %v, want %v", info.CanOpenSystemSettings, runtime.GOOS == "darwin")
	}
}

// The one-time notice fires exactly once for a denied permission —
// but only once DISPLAYED (fourth-audit finding 54): the check is a
// peek that keeps returning true until the dialog opens and marks
// it shown, so a notice claimed but never shown (app closed first)
// is not consumed.
func TestR28DeniedNoticeShownExactlyOnce(t *testing.T) {
	a := newR28TestApp(t, &fakeR28Notifier{status: notification.PermissionDenied})

	first, err := a.CheckNotificationDeniedNotice()
	if err != nil {
		t.Fatalf("unexpected error on first check: %v", err)
	}
	if !first {
		t.Fatal("first CheckNotificationDeniedNotice() = false, want true (denial first observed)")
	}

	// The peek does not consume the notice: a second check before
	// the dialog opens still reports it pending.
	second, err := a.CheckNotificationDeniedNotice()
	if err != nil {
		t.Fatalf("unexpected error on second check: %v", err)
	}
	if !second {
		t.Fatal("second CheckNotificationDeniedNotice() = false, want true (peek must not claim)")
	}

	shown, err := a.settingsStore.GetNotificationDeniedNoticeShown()
	if err != nil {
		t.Fatalf("unexpected error reading flag: %v", err)
	}
	if shown {
		t.Fatal("notice flag set before the dialog opened, want false")
	}

	// The dialog opened: mark it shown, and the notice is gone.
	if err := a.MarkNotificationDeniedNoticeShown(); err != nil {
		t.Fatalf("MarkNotificationDeniedNoticeShown: %v", err)
	}
	third, err := a.CheckNotificationDeniedNotice()
	if err != nil {
		t.Fatalf("unexpected error on third check: %v", err)
	}
	if third {
		t.Fatal("third CheckNotificationDeniedNotice() = true, want false (notice already shown)")
	}

	shown, err = a.settingsStore.GetNotificationDeniedNoticeShown()
	if err != nil {
		t.Fatalf("unexpected error reading flag: %v", err)
	}
	if !shown {
		t.Fatal("notice flag not persisted after marking shown, want true")
	}
}

// No notice when notifications are authorized — the user is not
// bothered about a problem that does not exist.
func TestR28NoticeNotShownWhenAuthorized(t *testing.T) {
	a := newR28TestApp(t, &fakeR28Notifier{status: notification.PermissionAuthorized})

	if show, err := a.CheckNotificationDeniedNotice(); err != nil || show {
		t.Errorf("CheckNotificationDeniedNotice() = %v, %v; want false, nil", show, err)
	}
}

// "unknown" is not denied: an unanswerable permission query must not
// trigger the denied notice (or the Settings warning).
func TestR28NoticeNotShownWhenStatusUnknown(t *testing.T) {
	a := newR28TestApp(t, &fakeR28Notifier{status: notification.PermissionUnknown})

	if show, err := a.CheckNotificationDeniedNotice(); err != nil || show {
		t.Errorf("CheckNotificationDeniedNotice() = %v, %v; want false, nil", show, err)
	}
}

// No notifier (and no settings store edge): the claim is a safe no-op,
// never a panic.
func TestR28NoticeNotShownWithoutNotifier(t *testing.T) {
	a := newR28TestApp(t, nil)

	if show, err := a.CheckNotificationDeniedNotice(); err != nil || show {
		t.Errorf("CheckNotificationDeniedNotice() = %v, %v; want false, nil", show, err)
	}

	info, err := a.GetNotificationPermissionStatus()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != string(notification.PermissionUnknown) {
		t.Errorf("Status without notifier = %q, want %q", info.Status, notification.PermissionUnknown)
	}
}

// Opening the system notification settings page from non-macOS fails
// loudly — the frontend only offers it on macOS, but the backend
// must not silently no-op if called anyway.
func TestR28OpenNotificationSettingsUnsupportedOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS-only affordance; covered by the darwin implementation")
	}
	a := newR28TestApp(t, &fakeR28Notifier{status: notification.PermissionDenied})
	if err := a.OpenNotificationSettings(); err == nil {
		t.Fatal("OpenNotificationSettings() on non-macOS returned nil, want an error")
	}
}
