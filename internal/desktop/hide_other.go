//go:build !windows

package desktop

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
