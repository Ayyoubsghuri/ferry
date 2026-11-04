//go:build !windows

package desktop

// OpenWindow shows Ferry in an app window (Chrome/Edge app mode, or the default browser).
func OpenWindow(url string) { OpenApp(url) }
