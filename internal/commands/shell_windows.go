package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// shellCommand runs a script line with cmd.exe. The command line is passed
// as is (cmd /d /s /c "<line>"): Go's argument escaping (\") is not what
// cmd.exe understands, and scripts such as go build -ldflags "-X a=b" would
// reach the program with broken quotes.
func shellCommand(line string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	cmd := exec.Command(comspec)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /d /s /c "` + line + `"`}
	return cmd
}
