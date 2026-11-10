package main

import (
	"bytes"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GTR_HOME", t.TempDir())
	exit := "exit 4"
	if runtime.GOOS == "windows" {
		exit = "exit /b 4"
	}
	os.WriteFile("gtr.json", []byte(`{"name":"xx","scripts":{"fail":"`+exit+`","hello":"echo hi from a script"}}`), 0o644)
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 0, "Usage:"},
		{[]string{"version"}, 0, "gtr-manager dev"},
		{[]string{"help"}, 0, "Packages:"},
		{[]string{"bogus"}, 1, "Unknown command: bogus"},
		{[]string{"ci"}, 1, "needs gtr.lock"},
		{[]string{"install"}, 0, "0 packages installed"},
		{[]string{"add"}, 1, "Error: specify a package"},
		{[]string{"run", "fail"}, 4, ""},
		{[]string{"start", "nope"}, 1, "not defined"},
		{[]string{"init"}, 1, "already initialized"},
		{[]string{"uninstall", "x"}, 1, "x is not in gtr.json"},
		{[]string{"new"}, 1, "project name"},
		{[]string{"instal"}, 1, "Did you mean `gtr install`?"},
		{[]string{"tset"}, 1, "Did you mean `gtr test`?"},
		{[]string{"hello"}, 0, "hi from a script"},               // a script as a command
		{[]string{"run", "install"}, 0, "running `gtr install`"}, // no script: the command
		{[]string{"run", "hell"}, 1, `did you mean "hello"?`},
	} {
		var out, errOut bytes.Buffer
		code := run(tc.args, &out, &errOut)
		if code != tc.code || !strings.Contains(out.String()+errOut.String(), tc.out) {
			t.Errorf("%v: code %d, out %q", tc.args, code, out.String()+errOut.String())
		}
	}
}
