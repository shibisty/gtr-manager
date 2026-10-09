package link

import (
	"os/exec"
	"syscall"
)

// junction creates a directory junction with mklink. The command line is
// built by hand so both paths are quoted: cmd.exe would otherwise treat
// characters such as & or ^ in a path as syntax.
func junction(link, target string) error {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/d /c mklink /J "` + link + `" "` + target + `"`}
	return cmd.Run()
}
