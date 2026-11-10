//go:build !windows

package commands

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// readPassword reads a line without echoing it when stdin is a terminal.
func readPassword(in *bufio.Reader) (string, error) {
	if f, ok := Stdin.(*os.File); ok && f == os.Stdin {
		if err := stty("-echo"); err == nil {
			defer stty("echo")
		}
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func stty(arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
