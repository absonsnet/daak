// Package tray provides Daak's system tray icon (Windows, macOS, Linux).
//
// The icon is shown whenever "Run in background" is on — including
// start-hidden launches — so a window hidden with CloseWindow or
// BeforeClose can always be reopened from the tray. Interaction:
//   - Left-click opens the menu on macOS; on Windows/Linux a double
//     left-click shows the window and a single left-click does nothing.
//   - Right-click opens the menu, in order: Open Daak (show the
//     window), Compose new message (show the window, then open the
//     composer), Check mail now (sync all accounts in the background,
//     window stays as it is), Settings (show the window, then open
//     the Settings dialog), Quit Daak (quit for real).
//
// It is built on fyne.io/systray via RunWithExternalLoop, the library's
// embedding API, so the tray coexists with Wails' own main loop (on
// macOS the tray piggybacks on Wails' NSApplication loop instead of
// taking over the app loop, which plain Run would do).
//
// Lifetime: the tray lives for the app session once started. Turning
// background mode off mid-session deliberately does NOT hide it: the
// library's Quit is once-only and its Windows window class cannot be
// re-registered, so a tray hidden that way could never be shown again —
// which would recreate the exact bug the tray exists to fix (a hidden
// window with no way back). The icon disappears when the app really
// quits (Stop, called from App.Shutdown; process exit removes it
// regardless).
//
// Death: the tray can also die mid-session without a Quit — the run
// loop exits (window destroyed, session ending, message-loop failure)
// and the library calls onExit. That closes Done() and flips Alive()
// to false, so the app can tell a dead tray from a live one instead
// of trusting the once-only started flag. A dead tray is NOT
// restarted in-process: the library's Quit is once-only (quitOnce)
// and re-registering the tray is untested native state on all three
// platforms, so the only safe response is to stop hiding the window
// on close and fall back to a real quit.
package tray

import (
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"

	"github.com/absonsnet/daak/internal/logging"
)

// doubleClickInterval is the maximum gap between two left-clicks that
// counts as a double-click (the Windows/macOS default double-click
// time is 500 ms).
const doubleClickInterval = 500 * time.Millisecond

// doubleClickDetector turns the library's per-click tap callback into a
// double-click callback: a lone tap does nothing, two taps within
// interval fire onDoubleClick. The library (fyne.io/systray v1.12.2) has
// no double-click callback of its own — SetOnTapped fires on every
// left-click — so the pairing is done here.
//
// No timer is needed: a stale first tap can never fire, because the
// interval check fails once it is old; it is simply overwritten by the
// next tap. A third rapid tap starts a new pair instead of re-firing.
type doubleClickDetector struct {
	mu            sync.Mutex
	interval      time.Duration
	lastTap       time.Time
	onDoubleClick func()
}

func (d *doubleClickDetector) tap() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	if !d.lastTap.IsZero() && now.Sub(d.lastTap) <= d.interval {
		d.lastTap = time.Time{}
		d.onDoubleClick()
		return
	}
	d.lastTap = now
}

// Tray owns the system tray icon for one app session.
type Tray struct {
	mu      sync.Mutex
	started bool
	end     func()

	// done is closed when the tray's run loop exits (see Start). It is
	// created fresh by each Start attempt, so a failed start that is
	// retried gets a new channel. The closer is a per-attempt closure
	// in Start that captures its own channel — a method on the struct
	// could not tell a late exit of an earlier attempt from the exit
	// of the current one, and would close the wrong channel.
	done chan struct{}

	// Unread badge state. The badge is the unified-inbox unread count
	// shown on the OS chrome: the macOS Dock icon (badgeLabel), the
	// Windows taskbar button (overlay icon) and the tray icon itself
	// (the number drawn onto the icon). The Dock/taskbar surfaces work
	// whether or not the tray is up; the tray icon part only applies
	// once Start has run. baseIconICO/baseIconPNG are the plain icons
	// Start was given, kept so disabling the badge (or a zero count)
	// can restore them.
	baseIconICO    []byte
	baseIconPNG    []byte
	badgeMu        sync.Mutex
	badgeLastCount int
	badgeEnabled   bool
}

// badgePushHook is called after every badge push that reaches the OS
// surfaces, with the count pushed (0 = cleared). Test-only: nil in
// production, set by tray tests to observe the update-only-on-change
// behaviour without a desktop.
var badgePushHook func(count int)

// New returns a Tray that has not been started yet.
func New() *Tray {
	return &Tray{badgeLastCount: -1, badgeEnabled: true}
}

// Done returns a channel that is closed when the tray's run loop exits.
// It is nil before the first Start — a nil channel blocks forever in a
// select, which is the correct "never exits" reading for a tray that
// never started. Callers that only need the current state want Alive().
func (t *Tray) Done() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

// Alive reports whether the tray icon is currently up: Start succeeded
// AND the run loop has not exited since. This is the check the close
// paths must use — the started flag alone is set once at startup and
// never cleared, so it still claims the tray is up after the tray has
// died mid-session. No polling: this is a single non-blocking channel
// read, taken only when the caller is deciding whether hiding the
// window is safe.
func (t *Tray) Alive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.aliveLocked()
}

// aliveLocked is Alive without the lock; the caller holds mu.
func (t *Tray) aliveLocked() bool {
	if !t.started || t.done == nil {
		return false
	}
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

// Start shows the tray icon. It is idempotent: later calls are no-ops.
// iconICO is the Windows .ico bytes, iconPNG the PNG bytes used on
// macOS/Linux; either may be empty, in which case the library's default
// icon is used. onShow shows the window (double left-click on
// Windows/Linux, or Open Daak); onQuit quits the app for real (Quit
// Daak); onCompose opens a new message (Compose new message); onCheckMail
// syncs all accounts in the background (Check mail now); onSettings opens
// the Settings dialog (Settings). A single left-click does nothing on
// Windows/Linux; on macOS a left-click shows the menu.
//
// It returns false when the tray could not be started — the caller must
// treat that as "no tray" and never leave the window hidden without one
// (a hidden window with no tray icon leaves the user locked out). A panic
// during start is recovered and reported the same way: a tray failure
// must never crash startup. Note this only catches Go panics, not a
// native crash — the real crash guard is invokeTrayStart, which runs the
// library's native start on the macOS main thread.
//
// A later call after the tray has died mid-session also returns false
// (Alive is re-checked, not just the started flag): a dead tray cannot
// be restarted in-process — the library's Quit is once-only and its
// Windows window class cannot be re-registered — so the caller must
// fall back to a real quit instead of believing the tray is back.
func (t *Tray) Start(iconICO, iconPNG []byte, onShow, onQuit, onCompose, onCheckMail, onSettings func()) (ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		// Already up — or already dead. A live tray is a no-op true;
		// a dead one reports false so the caller never hides the
		// window on the strength of a stale start.
		return t.aliveLocked()
	}

	log := logging.WithComponent("tray")

	// markExited closes this attempt's done channel, recording that
	// the tray's run loop has exited. Assigned once done is created
	// below; nil until then. It is a per-attempt closure (see the done
	// field comment) and is invoked from onExit, from the Windows
	// start path when systray.Run returns, and from the panic recovery
	// below. It never takes mu, so onExit cannot deadlock against a
	// caller that holds mu while invoking the library (Stop calls the
	// library's Quit under mu, and Quit runs onExit synchronously).
	var markExited func()

	defer func() {
		if r := recover(); r != nil {
			log.Error().Interface("panic", r).Msg("System tray failed to start; continuing without it")
			t.started = false
			t.end = nil
			if markExited != nil {
				markExited()
			}
			ok = false
		}
	}()

	// On Linux the tray is a D-Bus StatusNotifierItem: without a session
	// bus there is no tray host, and the library panics in its shutdown
	// path (it closes a nil connection) if started anyway. Skip starting
	// entirely in that case; a later Start retries.
	if runtime.GOOS == "linux" {
		conn, err := dbus.SessionBus()
		if err != nil {
			log.Warn().Err(err).Msg("No D-Bus session bus, skipping system tray icon")
			return false
		}
		conn.Close()
	}

	t.started = true
	// A fresh exit channel for this start attempt. A retry after a
	// failed start gets a new one; the old channel is simply abandoned
	// (nobody waits on it — liveness is only ever read via Alive()).
	// The closer captures this channel and a once guard of its own, so
	// a late exit from an earlier attempt can never close a later
	// attempt's channel.
	done := make(chan struct{})
	var exitOnce sync.Once
	markExited = func() {
		exitOnce.Do(func() { close(done) })
	}
	t.done = done
	t.baseIconICO = iconICO
	t.baseIconPNG = iconPNG
	icon := iconPNG
	if runtime.GOOS == "windows" {
		icon = iconICO
	}

	// readyCh is closed at the end of onReady, once the icon, the tap
	// handler and all five menu items are registered. The Windows
	// start waits on it (see startTrayWindows).
	readyCh := make(chan struct{})
	markReady := func() {
		select {
		case <-readyCh:
		default:
			close(readyCh)
		}
	}

	onReady := func() {
		if len(icon) > 0 {
			systray.SetIcon(icon)
		}
		systray.SetTooltip("Daak")
		// Left-click: on macOS no tap handler is registered, so the
		// library shows the menu itself on left-click (systray's darwin
		// code calls show_menu when no tap handler is set) — which is
		// also the macOS menu-bar convention. A double-click handler
		// here used to crash the app (v0.7.24): the tap callback fired
		// ShowWindow from the main thread, and ShowWindow's
		// "window:show" emit reached Wails ExecJS on the main thread,
		// which over-releases its script string (see App.ShowWindow
		// and docs/TRAY_AUDIT.md). Windows/Linux keep the v0.7.19
		// behaviour: double left-click shows the window, a single
		// left-click does nothing. The library has no double-click
		// callback, so taps are paired by doubleClickDetector.
		if runtime.GOOS != "darwin" {
			detector := &doubleClickDetector{interval: doubleClickInterval, onDoubleClick: onShow}
			systray.SetOnTapped(detector.tap)
		}
		// No SetOnSecondaryTapped: with no right-click handler the
		// library shows the menu itself on right-click (Windows and
		// macOS; on Linux the tray host renders the exported menu).
		// Setting one would replace the menu with the handler.

		openItem := systray.AddMenuItem("Open Daak", "Show the Daak window")
		composeItem := systray.AddMenuItem("Compose new message", "Write a new message")
		checkMailItem := systray.AddMenuItem("Check mail now", "Sync all accounts now")
		systray.AddSeparator()
		settingsItem := systray.AddMenuItem("Settings", "Open Daak settings")
		quitItem := systray.AddMenuItem("Quit Daak", "Quit Daak")
		go func() {
			for range openItem.ClickedCh {
				onShow()
			}
		}()
		go func() {
			for range composeItem.ClickedCh {
				onCompose()
			}
		}()
		go func() {
			for range checkMailItem.ClickedCh {
				onCheckMail()
			}
		}()
		go func() {
			for range settingsItem.ClickedCh {
				onSettings()
			}
		}()
		go func() {
			for range quitItem.ClickedCh {
				onQuit()
			}
		}()
		log.Info().Msg("System tray icon shown")
		markReady()
	}
	onExit := func() {
		log.Info().Msg("System tray icon hidden")
		// The run loop has exited — from here on the icon is gone,
		// whether this was a real Quit or a mid-session death (the
		// window destroyed, the session ending). Record it so Alive()
		// stops claiming the tray is up; the close paths then fall
		// back to a real quit instead of hiding the window with no
		// way back. Never takes mu (see markExited above).
		markExited()
	}
	// Windows: the tray window and its message pump must run on the
	// same OS thread (Win32 delivers a window's messages to the
	// creating thread's queue). RunWithExternalLoop creates the window
	// on the caller's goroutine and spawns the pump on a new goroutine,
	// so the pump can land on another thread and the window goes deaf:
	// the icon shows, but no click ever reaches wndProc (v0.7.31).
	// startTrayWindows runs systray.Run from a dedicated locked
	// thread instead, so window creation and pumping share the thread.
	if runtime.GOOS == "windows" {
		if !startTrayWindows(onReady, onExit, readyCh, markExited) {
			log.Warn().Msg("System tray did not start on its thread in time; continuing without it")
			t.started = false
			return false
		}
		t.end = func() { systray.Quit() }
		return true
	}
	start, end := systray.RunWithExternalLoop(onReady, onExit)
	// On macOS the library's native start creates AppKit objects and
	// must run on the main thread; invokeTrayStart dispatches it there
	// (elsewhere it runs directly). A late dispatch still creates the
	// tray afterwards, but the caller is told it is not up yet.
	if !invokeTrayStart(start) {
		log.Warn().Msg("System tray did not start on the main thread in time; continuing without it")
		t.started = false
		return false
	}
	t.end = end
	return true
}

// Stop ends the tray loop. App shutdown only — see the package comment
// for why turning background mode off does not call this.
func (t *Tray) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started || t.end == nil {
		return
	}
	t.end()
	t.started = false
	t.end = nil
}

// SetBadgeEnabled turns the unread badge on or off. Turning it off
// clears every badge surface at once; turning it back on resets the
// last-pushed count so the next SetBadge pushes even if the count is
// unchanged.
func (t *Tray) SetBadgeEnabled(enabled bool) {
	t.badgeMu.Lock()
	defer t.badgeMu.Unlock()
	if enabled == t.badgeEnabled {
		return
	}
	t.badgeEnabled = enabled
	t.badgeLastCount = -1
	if !enabled {
		t.pushBadgeLocked(0)
	}
}

// SetBadge pushes the unread count to the badge surfaces, but only when
// it changed since the last push — syncs and read flips can fire in
// bursts, and redrawing the tray icon on every one would flicker it.
// A disabled badge stays cleared. A failed push leaves the last count
// unrecorded so the next refresh retries.
func (t *Tray) SetBadge(count int) {
	if count < 0 {
		count = 0
	}
	t.badgeMu.Lock()
	defer t.badgeMu.Unlock()
	if !t.badgeEnabled {
		return
	}
	if count == t.badgeLastCount {
		return
	}
	if !t.pushBadgeLocked(count) {
		return
	}
	t.badgeLastCount = count
}

// waitForReady reports whether ready closed within timeout. The
// Windows start waits on it for the tray's menu to be registered.
func waitForReady(ready <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ready:
		return true
	case <-time.After(timeout):
		return false
	}
}

// pushBadgeLocked writes count to every badge surface. The caller holds
// badgeMu. It returns false if any surface failed.
func (t *Tray) pushBadgeLocked(count int) bool {
	log := logging.WithComponent("tray")
	ok := true
	if !setDockBadge(count) {
		log.Warn().Msg("Unread badge: Dock badge update failed; will retry on the next refresh")
		ok = false
	}
	if !setTaskbarOverlay(count) {
		log.Warn().Msg("Unread badge: taskbar overlay update failed; will retry on the next refresh")
		ok = false
	}
	t.mu.Lock()
	started := t.started
	baseICO := t.baseIconICO
	basePNG := t.baseIconPNG
	t.mu.Unlock()
	if started {
		if count <= 0 {
			// Plain icon again: the .ico on Windows, the PNG elsewhere.
			if len(baseICO) > 0 && runtime.GOOS == "windows" {
				systray.SetIcon(baseICO)
			} else if len(basePNG) > 0 {
				systray.SetIcon(basePNG)
			}
		} else {
			var icon []byte
			var err error
			if runtime.GOOS == "windows" {
				// The Windows tray loader only accepts .ico.
				icon, err = RenderBadgedIconICO(basePNG, count)
			} else {
				icon, err = RenderBadgedIconPNG(basePNG, count)
			}
			if err != nil {
				log.Error().Err(err).Msg("Unread badge: badged tray icon render failed")
				ok = false
			} else {
				systray.SetIcon(icon)
			}
		}
		systray.SetTooltip(BadgeTooltip(count))
	}
	if badgePushHook != nil {
		badgePushHook(count)
	}
	return ok
}
