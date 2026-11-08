//go:build !windows

package commands

import "os/exec"

// shellCommand runs a script line with sh.
func shellCommand(line string) *exec.Cmd { return exec.Command("sh", "-c", line) }
