//go:build !unix

package rehearse

import (
	"os/exec"
	"time"
)

func configureShellCmd(cmd *exec.Cmd) {
	cmd.WaitDelay = 500 * time.Millisecond
}
