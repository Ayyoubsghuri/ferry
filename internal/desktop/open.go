package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// OpenApp shows the UI in a chromeless browser window when Chrome/Edge is available,
// otherwise in the default browser.
func OpenApp(url string) {
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LocalAppData")} {
			for _, rel := range []string{`Microsoft\Edge\Application\msedge.exe`, `Google\Chrome\Application\chrome.exe`} {
				p := filepath.Join(base, rel)
				if base != "" && exists(p) {
					if start(p, "--app="+url, "--window-size=1100,800") == nil {
						return
					}
				}
			}
		}
		_ = start("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium"} {
			if exists("/Applications/" + app + ".app") {
				if start("open", "-na", app, "--args", "--app="+url, "--window-size=1100,800") == nil {
					return
				}
			}
		}
		_ = start("open", url)
	default:
		for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(b); err == nil {
				if start(p, "--app="+url, "--window-size=1100,800") == nil {
					return
				}
			}
		}
		_ = start("xdg-open", url)
	}
}

// OpenPath opens a folder in the file manager.
func OpenPath(p string) {
	switch runtime.GOOS {
	case "windows":
		_ = start("explorer", p)
	case "darwin":
		_ = start("open", p)
	default:
		_ = start("xdg-open", p)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func start(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
