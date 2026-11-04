package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SetStartAtLogin makes Ferry start (in the tray, without opening a window) when the user
// logs in, so the device stays discoverable and ready to receive.
func SetStartAtLogin(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		key := `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
		var cmd *exec.Cmd
		if on {
			cmd = exec.Command("reg", "add", key, "/v", "Ferry", "/t", "REG_SZ", "/d", fmt.Sprintf(`"%s" -no-open`, exe), "/f")
		} else {
			cmd = exec.Command("reg", "delete", key, "/v", "Ferry", "/f")
		}
		hideWindow(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil && on {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "darwin":
		p := filepath.Join(home, "Library", "LaunchAgents", "app.ferry.plist")
		if !on {
			_ = os.Remove(p)
			return nil
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>app.ferry</string>
  <key>ProgramArguments</key><array><string>` + exe + `</string><string>-no-open</string></array>
  <key>RunAtLoad</key><true/>
</dict></plist>
`
		return os.WriteFile(p, []byte(plist), 0o644)
	default:
		p := filepath.Join(home, ".config", "autostart", "ferry.desktop")
		if !on {
			_ = os.Remove(p)
			return nil
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		entry := "[Desktop Entry]\nType=Application\nName=Ferry\nExec=\"" + exe + "\" -no-open\nX-GNOME-Autostart-enabled=true\n"
		return os.WriteFile(p, []byte(entry), 0o644)
	}
}
