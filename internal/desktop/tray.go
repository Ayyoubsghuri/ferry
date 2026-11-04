//go:build (windows || linux || (darwin && cgo)) && !android

package desktop

import (
	"os"
	"runtime"

	"fyne.io/systray"
)

const TraySupported = true

type TrayInfo struct {
	Icon     []byte // PNG (macOS/Linux)
	IconICO  []byte // ICO (Windows)
	LocalURL string
	Shares   []string // addresses others can use
	DataDir  string
	OnQuit   func()
}

// RunTray blocks until the user quits from the tray menu.
func RunTray(info TrayInfo) {
	systray.Run(func() {
		if runtime.GOOS == "windows" {
			systray.SetIcon(info.IconICO)
		} else {
			systray.SetIcon(info.Icon)
		}
		systray.SetTooltip("Ferry is running")
		if runtime.GOOS != "darwin" {
			systray.SetTitle("Ferry")
		}
		systray.SetOnTapped(func() { OpenWindow(info.LocalURL) })

		open := systray.AddMenuItem("Open Ferry", "Show the Ferry window")
		systray.AddSeparator()
		hdr := systray.AddMenuItem("Other devices can open:", "")
		hdr.Disable()
		for _, u := range info.Shares {
			m := systray.AddMenuItem("   "+u, "Open this address on a phone or another computer")
			m.Disable()
		}
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit Ferry", "Stop sharing and quit")
		go func() {
			for {
				select {
				case <-open.ClickedCh:
					OpenWindow(info.LocalURL)
				case <-quit.ClickedCh:
					systray.Quit()
				}
			}
		}()
	}, func() {
		if info.OnQuit != nil {
			info.OnQuit()
		}
		os.Exit(0)
	})
}
