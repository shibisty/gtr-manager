# GTR Manager

GTR Manager is the package manager component of GTR.

It is responsible for:

- Project initialization
- Dependency management
- Script execution

GTR Manager is executed by the `gtr` launcher. End users normally interact only with the `gtr` command.

[![Patreon](https://c5.patreon.com/external/logo/become_a_patron_button.png)](https://www.patreon.com/cw/shibisty)

---

# Installation

You normally don't install GTR Manager yourself: `gtr` installs the version a
project asks for (`"engines": {"gtr": "^0.2"}` in `gtr.json`) from the GitHub releases
of this repository and runs it. All commands below work as `gtr <command>` and as
`gtr-manager <command>`.

```bash
gtr self install gtr 0.2      # install explicitly
gtr self list gtr             # installed versions
gtr-manager version
```

Working on GTR Manager itself — use your build instead of releases:

```bash
go build -o build/gtr-manager ./cmd/gtr-manager
gtr self link gtr ./build/gtr-manager
gtr self unlink gtr           # back to releases
```

## Releases

Pushing a tag `v1.2.3` runs `release.workflow.yml` (move it to
`.github/workflows/release.yml`): tests, then `./release.sh v1.2.3` builds
`gtr-manager_<version>_<os>_<arch>.zip|tar.gz` for Windows, Linux and macOS
(amd64, arm64) plus `SHA256SUMS`, and publishes them as a GitHub release. `gtr`
refuses archives that don't match `SHA256SUMS`.

---

# Project Manifest

Every project contains a `gtr.json` file.

Example:

```json
{
    "name": "hello-world",
    "version": "1.0.0",
    "author": "John Doe",
    "website": "https://example.com",
    "entrypoint": "main.go",

    "dependencies": {
        "github:golang/example": "*",
        "github:shibisty/faker.go": "0.1.0"
    },

    "scripts": {
        "start": "go run .",
        "build": "go build",
        "test": "go test ./..."
    }
}
```

---

# Commands

## Create a project

Create a new project.

```bash
gtr-manager new project-name
```

If the directory does not exist, it will be created.

If the directory exists, it must be empty.

---

## Initialize a project

Initialize the current directory.

```bash
gtr-manager init
```

Specify project name.

```bash
gtr-manager init my-project
```

During initialization several questions will be asked:

```
What is the package name?
What is the package version?
What is your name?
What is your website?
What is the entrypoint?
```

Accept all defaults without questions (scripts, CI):

```bash
gtr-manager init -y
gtr-manager new project-name -y
```

Fields of `gtr.json` that GTR does not know (`description`, `license`, …) are kept
when the file is rewritten.

---

## Install a package

Install a dependency from GitHub (default branch):

```bash
gtr-manager install github:golang/example
```

```json
"github:golang/example": "*"
```

Install a tag: `@1.2.0` looks for the tag `v1.2.0`, then `1.2.0`.

```bash
gtr-manager install github:golang/example@1.2.0
```

The package is unpacked into `packages/github/<owner>/<repo>`; `gtr.json` changes only
after a successful download. Archives are cached in `~/.gtr/cache/downloads`
(set `GTR_HOME` to move it).

For GitHub Enterprise or a mirror set `GTR_GITHUB_API` (and `GTR_GITHUB_TOKEN` for its
token; `GITHUB_TOKEN` is only ever sent to api.github.com).

Private repositories and the GitHub API limit (60 requests per hour without a token)
need a token:

```bash
export GITHUB_TOKEN=ghp_...        # PowerShell: $env:GITHUB_TOKEN = "ghp_..."
```

Not supported yet: version ranges (`^1`, `1.2`), GitLab/Bitbucket/git URLs and
packages from the GTR registry. These commands fail with a clear message instead of
silently writing to `gtr.json`.

---

## Uninstall a package

Remove a dependency and its folder.

```bash
gtr-manager uninstall github:golang/example
```

---

## Run scripts

Run the `start` script.

```bash
gtr-manager run
```

or

```bash
gtr-manager start
```

Run a named script.

```bash
gtr-manager run build
```

Pass extra arguments to the script (after `--`):

```bash
gtr-manager run test -- -run TestLogin -v
```

A missing script is an error that lists the available ones.

Example:

```json
{
    "scripts": {
        "start": "go run .",
        "build": "go build",
        "test": "go test ./..."
    }
}
```

---

# Package Sources

| Source | Example | Status |
|---|---|---|
| GitHub | `github:owner/repo`, `github:owner/repo@1.2.0` | Supported |
| GitLab, Bitbucket | `gitlab:group/repo` | Planned |
| Git URL | `https://git.example.com/repo.git` | Planned |
| GTR registry | `orm@^0.1` | Planned |

---

# Version Syntax

| Version | Meaning | Status |
|---|---|---|
| `*` | Default branch | Supported |
| `1.2.5`, `v1.2.5`, `1.2.5-rc.1` | Exact tag | Supported |
| `^1`, `1.2`, `>=1.0 <2` | Ranges | Planned (resolver) |

---

# Exit Codes

| Code | Description |
|------:|-------------|
| 0 | Success |
| 1 | Error |
| N | `run`: the exit code of the script |

---

# License

MIT License.

[![Patreon](https://c5.patreon.com/external/logo/become_a_patron_button.png)](https://www.patreon.com/cw/shibisty)

If this project helps you, consider supporting its development on Patreon ❤️