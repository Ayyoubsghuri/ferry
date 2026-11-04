//go:build !((windows || linux || (darwin && cgo)) && !android)

package desktop

const TraySupported = false

type TrayInfo struct {
	Icon     []byte
	IconICO  []byte
	LocalURL string
	Shares   []string
	DataDir  string
	OnQuit   func()
}

func RunTray(info TrayInfo) { select {} }
