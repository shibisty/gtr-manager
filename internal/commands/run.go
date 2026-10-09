package commands

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"gtr-manager/internal/manifest"
)

// Run runs a script from gtr.json ("start" by default). Arguments after the
// script name (and after an optional "--") are passed to the script:
//
//	gtr-manager run test -- -run TestX
//
// If the script fails, an *exec.ExitError is returned and main exits with
// the same code.
func Run(args []string) error {
	m, err := manifest.Load(manifest.FileName)
	if err != nil {
		return err
	}
	name := "start"
	if len(args) > 0 {
		name, args = args[0], args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	command := strings.TrimSpace(m.Scripts[name])
	if command == "" {
		return fmt.Errorf("script %q is not defined in gtr.json%s", name, available(m.Scripts))
	}
	for _, a := range args {
		command += " " + quote(a)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = Stdin, Stdout, Stderr
	cmd.Env = os.Environ()
	fmt.Fprintf(Stderr, "> %s\n\n", command)
	return cmd.Run()
}

func available(scripts map[string]string) string {
	names := make([]string, 0, len(scripts))
	for n, c := range scripts {
		if strings.TrimSpace(c) != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return "; available: " + strings.Join(names, ", ")
}

// quote escapes an argument for sh or cmd.exe.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`&|;<>()*?[]{}!#~%^") {
		return s
	}
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
