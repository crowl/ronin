//go:build unix || darwin || linux

package shell

import "os/exec"

func createCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}
