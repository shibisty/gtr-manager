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
    new <name> [-y] [-t <template>]  Create a project (templates: app, cli, lib, orm-driver, none)
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
    list / ls [--tree|--depth N]     Installed packages from gtr.lock (--json)
    why <name>                       Which requirements bring a package in
    outdated [--all]                 Packages with newer versions (--all: also indirect)

Scripts:
    run / start [name] [-- args...]  Run a script from gtr.json ("start" by default), with
                                     pre<name>/post<name> and variables from .env, .env.local
    run -r <name> [-- args...]       Run it in every workspace member that defines it
    run --no-env <name>              Without the .env files

Build and test:
    build [-t os/arch]... [--all-targets] [--archive]
                                     Build by the "build" block of gtr.json into
                                     build/<version>/<os>-<arch>/ ({{version}}, {{commit}},
                                     {{date}} in ldflags); --archive: .zip/.tar.gz + SHA256SUMS
    dev [-- args...]                 Build, run, and rebuild and restart on changes
    test [--unit|--integration|--e2e] [--coverage [--html]] [--watch] [-r]
                                     go test ./... with build tags; the coverage threshold
                                     is "test": {"coverage": 70} in gtr.json

Releases:
    version                          The package version (and gtr-manager's)
    version patch|minor|major|prerelease|<x.y.z> [--preid rc] [--no-git] [--no-tag]
                                     Bump gtr.json and CHANGELOG.md, commit, tag v<x.y.z>
                                     (<name>@<x.y.z> in a subdirectory of the repository)
    publish [--dry-run] [--skip-tests]
                                     Check (clean, tagged HEAD, tests), push branch and tag

Local development:
    link                             In a package: register it for gtr link <name>
    link <name|path>                 In a project: use that local copy (not in gtr.lock)
    unlink [name] [--global]         Stop using local copies (--global: unregister)

    init --ci                        Add .github/workflows/ci.yml (gtr ci && gtr test --coverage)
    -v, --version                    Show the gtr-manager version
    help                             Show this help

Sources:
    github:owner/repo                Newest tag (saved as ^<version>); untagged: default branch
    github:owner/repo#^0.2           A range of tags: v0.2.x
    github:owner/repo/sub/dir#^0.1   A package in a monorepo subdirectory
    gitlab:group/project[//sub]      GitLab (GTR_GITLAB_URL, GTR_GITLAB_TOKEN)
    git+https://host/repo.git[//sub] Any git server through git (also git+ssh://, git+file://)
    file:../path                     A local package, linked (not copied)
    name=github:owner/repo           Check that the package has this name
    workspace:*                      A member of the workspace (gtr add <member>)

Workspaces: "workspaces": ["core", "drivers/*"] in the root gtr.json. Members are used
in place instead of any other source; commands in a member act on the whole workspace.

Packages are linked into gtr_modules/ from ~/.gtr/store; go.mod is generated.
Scripts run go with GOPROXY=off, GOWORK=off and -mod=mod; go errors get gtr hints.
GITHUB_TOKEN: private repositories and higher API limits.
--offline (install, add, update, ci, sync, remove) or GTR_OFFLINE=1: only what the store has.
`, version)
}
