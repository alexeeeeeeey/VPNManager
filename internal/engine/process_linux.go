package engine

import (
	"os/exec"
	"syscall"
)

func tieProcessToManager(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
