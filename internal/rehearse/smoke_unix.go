//go:build unix

package rehearse

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureShellCmd(cmd *exec.Cmd) {
	// The shell's children inherit its output pipes. Killing only the shell
	// leaves those pipes open, and Wait then blocks until the child exits.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 500 * time.Millisecond
	cmd.Cancel = func() error {
		return killProcessGroup(cmd.Process)
	}
}

func killProcessGroup(proc *os.Process) error {
	if proc == nil {
		return os.ErrProcessDone
	}
	groupErr := syscall.Kill(-proc.Pid, syscall.SIGKILL)
	procErr := proc.Kill()
	if groupErr == nil || procErr == nil || errors.Is(groupErr, syscall.ESRCH) || errors.Is(procErr, syscall.ESRCH) {
		return nil
	}
	return procErr
}
