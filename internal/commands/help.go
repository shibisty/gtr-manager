package commands

import (
	"fmt"
	"io"
)

// Help prints usage information.
func Help(w io.Writer, version string) {
	fmt.Fprintf(w, `gtr-manager %s

Usage:
    gtr-manager <command> [arguments]

Commands:
    new <name> [-y]                     Create a new project
    init [name] [-y]                    Initialize the current directory (-y: accept defaults)

    install / i / add / require <pkg>   Install a package into packages/
    uninstall / ui / u / remove / rm    Uninstall a package

    run / start                         Run the "start" script
    run / start <name> [-- args...]     Run the specified script with extra arguments

    version                             Show the current version
    help                                Show this help message

Packages:
    github:owner/repo                   Default branch
    github:owner/repo@1.2.0             Tag v1.2.0 (or 1.2.0)
    Set GITHUB_TOKEN for private repositories and higher API limits.

Coming later: new --repo, gtr registry packages, version ranges (^1, 1.2).
`, version)
}
