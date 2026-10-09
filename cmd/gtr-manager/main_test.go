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
	exit := "exit 4"
	if runtime.GOOS == "windows" {
		exit = "exit /b 4"
	}
	os.WriteFile("gtr.json", []byte(`{"name":"x","scripts":{"fail":"`+exit+`"}}`), 0o644)
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{nil, 0, "Usage:"},
		{[]string{"version"}, 0, "gtr-manager dev"},
		{[]string{"help"}, 0, "Commands:"},
		{[]string{"bogus"}, 1, "Unknown command: bogus"},
		{[]string{"install"}, 1, "Error: package name is required"},
		{[]string{"run", "fail"}, 4, ""},
		{[]string{"start", "nope"}, 1, "not defined"},
		{[]string{"init"}, 1, "already initialized"},
		{[]string{"uninstall", "x"}, 1, "not installed"},
		{[]string{"new"}, 1, "project name"},
	} {
		var out, errOut bytes.Buffer
		code := run(tc.args, &out, &errOut)
		if code != tc.code || !strings.Contains(out.String()+errOut.String(), tc.out) {
			t.Errorf("%v: code %d, out %q", tc.args, code, out.String()+errOut.String())
		}
	}
}
