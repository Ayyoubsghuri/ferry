//go:build !windows

package desktop

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Clipboard uses the platform's command-line clipboard tools.
type Clipboard struct {
	read  []string
	write []string
}

// NewClipboard returns nil when no clipboard tool is available.
func NewClipboard() *Clipboard {
	switch runtime.GOOS {
	case "darwin":
		return &Clipboard{read: []string{"pbpaste"}, write: []string{"pbcopy"}}
	case "linux":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if _, err := exec.LookPath("wl-paste"); err == nil {
				return &Clipboard{read: []string{"wl-paste", "--no-newline", "--type", "text"}, write: []string{"wl-copy"}}
			}
		}
		if os.Getenv("DISPLAY") != "" {
			if _, err := exec.LookPath("xclip"); err == nil {
				return &Clipboard{read: []string{"xclip", "-selection", "clipboard", "-o"}, write: []string{"xclip", "-selection", "clipboard"}}
			}
			if _, err := exec.LookPath("xsel"); err == nil {
				return &Clipboard{read: []string{"xsel", "--clipboard", "--output"}, write: []string{"xsel", "--clipboard", "--input"}}
			}
		}
	}
	return nil
}

func (c *Clipboard) Read() (string, error) {
	if c == nil {
		return "", errors.New("no clipboard")
	}
	out, err := exec.Command(c.read[0], c.read[1:]...).Output()
	if err != nil {
		return "", nil // empty or non-text clipboard
	}
	return strings.TrimSuffix(string(out), "\x00"), nil
}

func (c *Clipboard) Write(s string) error {
	if c == nil {
		return errors.New("no clipboard")
	}
	cmd := exec.Command(c.write[0], c.write[1:]...)
	cmd.Stdin = bytes.NewBufferString(s)
	return cmd.Run()
}
