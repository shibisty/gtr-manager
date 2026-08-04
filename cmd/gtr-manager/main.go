package main

import (
    "fmt"
    "os"
    "gtr-manager/internal/commands"
)

var Version = "dev"

func main() {

    if len(os.Args) < 2 {
        commands.Help(Version)
        return
    }

    var err error

    switch os.Args[1] {

    case "new":
        err = commands.New(os.Args[2:])

    case "init":
        err = commands.Init(os.Args[2:])

    case "install", "i", "add", "require":
        err = commands.Install(os.Args[2:])

    case "uninstall", "ui", "u", "remove", "rm":
        err = commands.Uninstall(os.Args[2:])

    case "run", "start":
        err = commands.Run(os.Args[2:])

    case "version", "-v", "--version":
        fmt.Println("gtr-manager", Version)

    case "help", "-h", "--help":
        commands.Help(Version)

    default:
        fmt.Printf("Unknown command: %s\n\n", os.Args[1])
        commands.Help(Version)
        os.Exit(1)
    }

    if err != nil {
        fmt.Fprintln(os.Stderr, "Error:", err)
        os.Exit(1)
    }
}
