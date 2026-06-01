//go:build !unix && !darwin && !linux

package shell

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
