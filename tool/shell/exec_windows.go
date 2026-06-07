//go:build windows

package shell

import (
	"os"
	"os/exec"
)

func createCommand(command string) *exec.Cmd {
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return exec.Command("powershell.exe", "-Command", command)
	}
	shell := os.Getenv("COMSPEC")
	if shell == "" {
		shell = "cmd.exe"
	}
	return exec.Command(shell, "/c", command)
}
