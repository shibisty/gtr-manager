//go:build windows

package commands

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

const enableEchoInput = 0x0004

var setConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// readPassword reads a line without echoing it when stdin is a console.
func readPassword(in *bufio.Reader) (string, error) {
	if f, ok := Stdin.(*os.File); ok && f == os.Stdin {
		h := syscall.Handle(os.Stdin.Fd())
		var mode uint32
		if syscall.GetConsoleMode(h, &mode) == nil {
			setConsoleMode.Call(uintptr(h), uintptr(mode&^enableEchoInput))
			defer setConsoleMode.Call(uintptr(h), uintptr(mode))
		}
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
