package commands

import (
	"fmt"
	"io"
)

// Help prints the usage.
func Help(w io.Writer, version string) {
	fmt.Fprintf(w, `gtr-manager %s

Usage:
    gtr <command> [arguments]        (gtr forwards these commands to gtr-manager)

Project:
    new <name> [-y]                  Create a project
    init [name] [-y]                 Create gtr.json in the current directory

Packages:
    install / i                      Install everything gtr.json asks for (uses gtr.lock)
    add <source>... [-D]             Add packages (-D: devDependencies)
    install <source>... [-D]         Same as add
    remove / uninstall <name>...     Remove packages
    update [name]...                 Move packages to the newest versions their ranges allow
    ci                               Install exactly gtr.lock; fail if it does not match gtr.json
    sync                             Carry go.mod edits (or an existing go.mod) over to gtr.json
    sync --force                     Regenerate go.mod from gtr.json, discarding edits

Scripts:
    run / start [name] [-- args...]  Run a script from gtr.json ("start" by default)
    run -r <name> [-- args...]       Run it in every workspace member that defines it

    version                          Show the version
    help                             Show this help

Sources:
    github:owner/repo                Newest tag (saved as ^<version>); untagged: default branch
    github:owner/repo#^0.2           A range of tags: v0.2.x
    github:owner/repo/sub/dir#^0.1   A package in a monorepo subdirectory
    file:../path                     A local package, linked (not copied)
    name=github:owner/repo           Check that the package has this name
    workspace:*                      A member of the workspace (gtr add <member>)

Workspaces: "workspaces": ["core", "drivers/*"] in the root gtr.json. Members are used
in place instead of any other source; commands in a member act on the whole workspace.

Packages are linked into gtr_modules/ from ~/.gtr/store; go.mod is generated.
Scripts run go with GOPROXY=off, GOWORK=off and -mod=mod; go errors get gtr hints.
GITHUB_TOKEN: private repositories and higher API limits.
`, version)
}
