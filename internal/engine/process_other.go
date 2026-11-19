//go:build !linux

package engine

import "os/exec"

func tieProcessToManager(cmd *exec.Cmd) {}
