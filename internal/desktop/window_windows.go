package desktop

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"

	webview2 "ferry/internal/winview"
	"ferry/internal/winview/webviewloader"
)

// Windows: Ferry opens in its own frameless window (WebView2, built into Windows 10/11).
// The page draws the title bar and window buttons, so the whole window matches Ferry's
// design. If WebView2 isn't available, it falls back to an Edge app window.

var (
	winUser32           = syscall.NewLazyDLL("winUser32.dll")
	pShowWindow      = winUser32.NewProc("ShowWindow")
	pSetForeground   = winUser32.NewProc("SetForegroundWindow")
	pIsZoomed        = winUser32.NewProc("IsZoomed")
	pReleaseCapture  = winUser32.NewProc("ReleaseCapture")
	pSendMessage     = winUser32.NewProc("SendMessageW")
	pPostMessage     = winUser32.NewProc("PostMessageW")
	winMu            sync.Mutex
	winHwnd          uintptr
)

const (
	swMinimize = 6
	swMaximize = 3
	swRestore  = 9
)

// OpenWindow shows the Ferry window, creating it if needed.
func OpenWindow(url string) {
	winMu.Lock()
	if winHwnd != 0 {
		h := winHwnd
		winMu.Unlock()
		pShowWindow.Call(h, swRestore)
		pSetForeground.Call(h)
		return
	}
	winMu.Unlock()
	if v, err := webviewloader.GetInstalledVersion(); err != nil || v == "" {
		OpenApp(url)
		return
	}
	ready := make(chan bool, 1)
	go func() {
		runtime.LockOSThread()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("window: %v", r)
				select {
				case ready <- false:
				default:
				}
			}
		}()
		base, _ := os.UserCacheDir()
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  filepath.Join(base, "Ferry", "WebView2"),
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title: "Ferry", Width: 1180, Height: 820, Center: true, IconId: 2, Frameless: true,
			},
		})
		if w == nil {
			ready <- false
			return
		}
		hwnd := uintptr(w.Window())
		w.SetSize(760, 560, webview2.HintMin)
		_ = w.Bind("ferryWinMin", func() { pShowWindow.Call(hwnd, swMinimize) })
		_ = w.Bind("ferryWinMax", func() bool {
			if z, _, _ := pIsZoomed.Call(hwnd); z != 0 {
				pShowWindow.Call(hwnd, swRestore)
				return false
			}
			pShowWindow.Call(hwnd, swMaximize)
			return true
		})
		_ = w.Bind("ferryWinIsMax", func() bool { z, _, _ := pIsZoomed.Call(hwnd); return z != 0 })
		_ = w.Bind("ferryWinClose", func() { pPostMessage.Call(hwnd, 0x0010, 0, 0) }) // WM_CLOSE
		_ = w.Bind("ferryWinDrag", func() {
			// Hand the mouse to Windows as if the title bar was grabbed (moves, snaps, shakes).
			pReleaseCapture.Call()
			pSendMessage.Call(hwnd, 0x00A1, 2, 0) // WM_NCLBUTTONDOWN, HTCAPTION
		})
		w.Navigate(url)
		winMu.Lock()
		winHwnd = hwnd
		winMu.Unlock()
		ready <- true
		w.Run()
		winMu.Lock()
		winHwnd = 0
		winMu.Unlock()
	}()
	if !<-ready {
		OpenApp(url)
	}
}
