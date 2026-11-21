//go:build !linux

package engine

import "os/exec"

func detach(*exec.Cmd) {}
