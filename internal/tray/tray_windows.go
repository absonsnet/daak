//go:build windows

package tray

import (
	"runtime"
	"time"

	"fyne.io/systray"
)

// startTrayWindows runs the tray on a dedicated OS thread and waits
// until its menu is registered (or the timeout expires).
//
// Win32 delivers a window's messages to the message queue of the
// thread that created the window, and GetMessage only retrieves
// messages for the calling thread. RunWithExternalLoop creates the
// tray window on the caller's goroutine and spawns the message pump
// on a new goroutine, so the pump can land on another OS thread: the
// icon still shows (Shell_NotifyIcon is a one-shot registration) but
// the window procedure never runs, so no click — left, right or
// double — ever reaches the menu or the double-click detector. That
// is why the tray menu never showed on Windows (v0.7.17–v0.7.30):
// the icon was there, the menu items were registered, and every click
// was silently dropped. Wails calls OnStartup (hence Tray.Start) on
// a worker goroutine, and nothing pins the pump goroutine to the
// window's thread, so the external-loop start is a race the tray
// loses.
//
// systray.Run pumps on the calling goroutine, so running it from a
// goroutine locked to its OS thread keeps window creation and
// pumping on the same thread, deterministically. (setInternalLoop is
// a no-op on Windows, so Run is exactly Register + pump on the
// caller.) Ready is signalled at the end of onReady, once the icon,
// the tap handler and all five menu items are registered; a timeout
// only means this call does not wait for the tray — a late start
// still brings it up afterwards.
//
// onRunReturn is called when systray.Run returns — i.e. the tray's
// message loop has ended and the icon is gone. The library calls
// onExit on the normal exit paths (Quit, window destroyed, session
// ending), but a message-loop failure (GetMessage returning -1) ends
// the loop without ever calling onExit, so the return is the only
// signal for that death. Calling it is safe on every path: it is
// guarded to run once per start attempt, and onExit already called
// it on the paths where both fire.
func startTrayWindows(onReady, onExit func(), ready <-chan struct{}, onRunReturn func()) bool {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		systray.Run(onReady, onExit)
		if onRunReturn != nil {
			onRunReturn()
		}
	}()
	return waitForReady(ready, 5*time.Second)
}
