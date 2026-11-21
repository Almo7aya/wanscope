//go:build linux

package engine

import (
	"os/exec"
	"syscall"
)

// detach starts the update in a session of its own, so it outlives wanscope's restart.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
