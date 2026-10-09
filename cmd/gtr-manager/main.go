package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"gtr-manager/internal/commands"
	"gtr-manager/internal/repositories"
)

// Version is set at build time: -ldflags "-X main.Version=0.0.1".
var Version = "dev"

func main() {
	commands.Sources["github"] = func() (commands.Installer, error) { return repositories.NewGitHub() }
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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
	case "install", "i", "add", "require":
		err = commands.Install(args[1:])
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
