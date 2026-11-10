package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"gtr-manager/internal/commands"
)

// commandNames are the commands and their synonyms, for "did you mean".
var commandNames = []string{"new", "init", "install", "add", "require", "update", "upgrade", "ci", "sync",
	"uninstall", "remove", "run", "start", "list", "why", "outdated", "build", "test", "dev", "version",
	"publish", "link", "unlink", "login", "logout", "whoami", "search", "info", "yank", "unyank", "owner", "help"}

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
	case "list", "ls":
		err = commands.List(args[1:])
	case "why":
		err = commands.Why(args[1:])
	case "outdated":
		err = commands.Outdated(args[1:])
	case "build":
		err = commands.Build(args[1:])
	case "test":
		err = commands.Test(args[1:])
	case "dev":
		err = commands.Dev(args[1:])
	case "version":
		err = commands.Version(args[1:], Version)
	case "publish":
		err = commands.Publish(args[1:])
	case "login":
		err = commands.Login(args[1:])
	case "logout":
		err = commands.Logout(args[1:])
	case "whoami":
		err = commands.Whoami(args[1:])
	case "search":
		err = commands.Search(args[1:])
	case "info", "view":
		err = commands.Info(args[1:])
	case "yank":
		err = commands.Yank(args[1:], false)
	case "unyank":
		err = commands.Yank(args[1:], true)
	case "owner", "owners":
		err = commands.Owner(args[1:])
	case "link":
		err = commands.Link(args[1:])
	case "unlink":
		err = commands.Unlink(args[1:])
	case "-v", "--version":
		fmt.Fprintln(stdout, "gtr-manager", Version)
	case "help", "-h", "--help":
		commands.Help(stdout, Version)
	default:
		// A script of the project (gtr migrate → gtr run migrate), as yarn does.
		if commands.HasScript(args[0]) {
			err = commands.Run(args)
			break
		}
		fmt.Fprintf(stderr, "Unknown command: %s\n", args[0])
		if s := commands.Suggest(args[0], commandNames); s != "" {
			fmt.Fprintf(stderr, "Did you mean `gtr %s`?\n", s)
		}
		fmt.Fprintln(stderr)
		commands.Help(stderr, Version)
		return 1
	}
	if err == nil {
		return 0
	}
	if code, silent := commands.ExitCode(err); silent {
		return code // already reported (failing tests)
	} else if code > 1 {
		fmt.Fprintln(stderr, "Error:", err)
		return code
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode() // the script has already printed its error
	}
	fmt.Fprintln(stderr, "Error:", err)
	return 1
}
