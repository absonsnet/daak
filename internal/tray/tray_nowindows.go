//go:build !windows

package tray

// startTrayWindows is a no-op off Windows: only the Windows start
// runs the tray on a dedicated locked thread (see tray_windows.go).
// It is never called — Tray.Start only calls it when
// runtime.GOOS == "windows" — but the reference must resolve on
// every platform.
func startTrayWindows(onReady, onExit func(), ready <-chan struct{}, onRunReturn func()) bool {
	return false
}
