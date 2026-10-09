package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"gtr-manager/internal/commands"
)

// Version is set at build time: -ldflags "-X main.Version=0.0.1".
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	commands.Stdout, commands.Stderr = stdout, stderr
	if len(args) == 0 {
		commands.Help(stdout, Version)
		return 0
	}
	var err error
	switch args[0] {
	case "new":
		err = commands.New(args[1:])
	case "init":
		err = commands.Init(args[1:])
	case "install", "i":
		err = commands.Install(args[1:])
	case "add", "require":
		err = commands.Add(args[1:])
	case "update", "up", "upgrade":
		err = commands.Update(args[1:])
	case "ci":
		err = commands.CI(args[1:])
	case "sync":
		err = commands.Sync(args[1:])
	case "uninstall", "ui", "u", "remove", "rm":
		err = commands.Uninstall(args[1:])
	case "run", "start":
		err = commands.Run(args[1:])
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, "gtr-manager", Version)
	case "help", "-h", "--help":
		commands.Help(stdout, Version)
	default:
		fmt.Fprintf(stderr, "Unknown command: %s\n\n", args[0])
		commands.Help(stderr, Version)
		return 1
	}
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode() // the script has already printed its error
	}
	fmt.Fprintln(stderr, "Error:", err)
	return 1
}
