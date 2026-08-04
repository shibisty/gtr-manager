package commands

import "fmt"

func Help(version string) {

    fmt.Printf(`gtr %s

Usage:
    gtr <command> [arguments]

Commands:
    new <name>                          Create a new project
    new <name> --repo <repo>            Create a project from a repository

    init                                Initialize the current directory
    init <name>                         Initialize the current directory with the specified project name

    install / i / add / require         Install a package to the packages directory
    uninstall / ui / u / remove / rm    Uninstall a package from the packages directory

    run / start                         Run the "start" script, if it exists
    run / start <name>                  Run the specified script

    version                             Show the current version
    help                                Show this help message
`, version)

}
