//go:build windows

package shell

import (
	"os/exec"
	"strconv"
)

func configureProcessGroup(_ *exec.Cmd) {
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
