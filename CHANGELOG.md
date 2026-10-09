# Changelog

## Unreleased

### Added (stage 4, workspaces — ADR-0009)
- `"workspaces": ["core", "drivers/*", "!drivers/old"]` in a root `gtr.json`. Members are
  used in place instead of any other source of their name (ranges still checked), so a
  member's committed `gtr.json` keeps its real source; `workspace:*` / `workspace:<range>`
  name a member explicitly (`gtr add <member>`).
- One resolution for the whole workspace from the root (members' `devDependencies`
  included): one `gtr.lock` and `gtr_modules/` at the root; generated `go.mod` in the root
  and every member, and `go.work` at the root (`use` + `replace`); hand-written or edited
  files are refused unless `gtr sync --force`. A `go.work` left from a former workspace is
  removed.
- Commands in a member act on the workspace; `gtr add`/`remove` change the member's
  `gtr.json` (saved only after a successful install). `gtr run -r <script>` runs a script
  in every member that defines it, dependencies first. Scripts get `GOWORK=<root>/go.work`
  instead of `GOWORK=off` and no `-mod=mod`.
- A member's generated `go.mod` lists only what the member needs (not the whole workspace
  build list), and a member used as a `file:` package by another workspace keeps it, so
  sibling families can share packages (`"orm": "file:../orm.go/core"`).

### Fixed
- `gtr run -r` in a workspace reached through a symlink (macOS temp and home paths under
  `/var` → `/private/var`): the script gets `PWD` set to the member directory, so the go
  command sees it under the same path as `go.work` ("directory prefix . does not contain
  modules listed in go.work").
- An untagged repository (`0.0.0-g<sha>`) satisfies peer ranges during resolution too,
  as the final peer check already allowed: a strategy with peer `passport ^0.1` installs
  before passport is tagged.

### Added (stage 3, step 3)
- **`gtr sync`** carries manual `go.mod` edits over to `gtr.json` — new and removed
  `require`s of modules, versions outside the current range, `replace <package> => ../dir`
  as a `file:` source, an indirect module raised above what gtr selects (it becomes a
  dependency), a go directive above the generated one (`engines.go`) — and imports a
  `go.mod` gtr did not write (existing Go projects), keeping its module versions. Edits it
  cannot carry (a gtr package without a source, a module replaced by a directory or by
  another module, a gtr package version changed in `go.mod`, a renamed module, `tool`,
  `exclude`, `godebug` and `ignore` directives, an `engines.go` with an upper bound) are
  listed, and nothing is changed. `gtr.json` is now saved before `go.mod` is written.
  `gtr sync --force` still discards edits.
- **go environment**: in a project with a generated `go.mod`, `gtr run` scripts get
  `GOPROXY=off`, `GOWORK=off` and `-mod=mod` (unless `GOFLAGS` sets `-mod`; `GOFLAGS`
  from `go env -w` is kept). `gtr run` survives Ctrl+C until the script exits, and does
  not wait for background processes that keep its output open.
- **gtr hints after go errors** (`package X is not in std`, `missing dot in first path
  element`, `no required module provides package`, `module lookup disabled by
  GOPROXY=off`, `ambiguous import`, `updates to go.mod needed`) in `gtr run` output, unless
  stderr is a terminal.
- Package names taken by the Go standard library (`go list std` of the active Go, cached
  per Go version) are rejected by `gtr init` and `gtr install`.
- `gtr init` in a directory with a `go.mod` offers its module path as the name and points
  to `gtr sync`.

### Added (stage 3, step 2)
- **External Go modules** keyed by module path (`"github.com/go-sql-driver/mysql": "^1.8"`),
  in the project and in gtr packages: GOPROXY protocol (`GTR_GOPROXY`, `GOPROXY`,
  `GTR_GOPROXY_PRIVATE` for `GOPRIVATE`/`GONOPROXY` modules), versions by range like
  `go get` (retractions, `+incompatible` last), then minimal version selection to the same
  build list as the go command; `go.mod` and zip hashes pinned in `gtr.lock` (`integrity`,
  `goMod`) and checked against the checksum database (`GTR_GOSUMDB`, `GOSUMDB`,
  `GONOSUMDB`, `GOPRIVATE`); `.mod` files cached for offline reinstalls and re-checked on
  every use; modules stored in `~/.gtr/store/go` and linked into `gtr_modules/.go/`.
- The project `go` directive uses Go's version ordering (`1.22` < `1.22.0`), so modules
  with a full `go 1.22.0` line no longer make the go command rewrite go.mod.
- `gtr add github.com/x/y[@range]`; the project `go` directive also covers the modules'
  `go` lines.

### Added (stage 3, step 1)
- **New `gtr.json` format (ADR-0003):** the key is the import path, the value the source —
  `"orm": "github:shibisty/orm.go#^0.1"`, `"orm-mysql": "github:shibisty/orm.go/drivers/mysql"`,
  `"passport": "file:../passport.go"`. Old files are migrated on the next install
  (`"github:owner/repo"` keys, `engine`, `website`, `entrypoint`), with warnings.
- **Resolver (ADR-0004):** versions from git tags (monorepo prefixes `<name>@x.y.z`,
  `<subdir>/vx.y.z`), npm-style ranges, one version per package, backtracking with a
  conflict report, locked versions preferred, `peerDependencies` checked, the package name
  must match the key.
- **`gtr.lock`** with version, source, commit and `h1:` integrity (the go.sum algorithm).
  A locked tag that now points at another commit stops the install until
  `gtr update <name>`; an untagged dependency stays on its locked commit; `gtr ci`
  re-hashes stored packages and fails if one was modified through `gtr_modules/`.
- **Store (ADR-0005):** packages are stored once in `~/.gtr/store/gtr/<name>@<version>-<hash>`,
  immutable, with a generated `go.mod`.
- **`gtr_modules/`** links (junctions on Windows, copies as a fallback) and a generated,
  canonical `go.mod` with a content hash; manual edits are detected, go's own bookkeeping
  (`// indirect` moves, `toolchain` lines) is not (ADR-0001). Entries in `gtr_modules/`
  that gtr did not create are left alone.
- Commands: `install` (without arguments: everything), `add [-D]`, `remove`, `update [name]`,
  `ci`, `sync --force`.
- `init` writes a schema-valid `gtr.json` (`engines.go`, `homepage`), validates the package
  name and adds generated files to `.gitignore`.

### Changed
- `install <source>` is `add <source>`; packages go to `gtr_modules/` instead of `packages/`.
- Untagged repositories are installed from the default branch only for the range `*`.

### Added (stage 2)
- `release.sh` and `release.workflow.yml`: release archives and `SHA256SUMS` in the
  format `gtr` installs from (ADR-0008).
- `GTR_GITHUB_API` (+ `GTR_GITHUB_TOKEN`) — GitHub Enterprise or a mirror for package
  downloads; `GITHUB_TOKEN` is never sent to it.

### Fixed
- `uninstall` never found packages: dependencies were saved as `github:owner/repo`
  but looked up as `owner/repo`. The same mismatch let `install` add a duplicate.
- `install` wrote `gtr.json` before downloading: a failed download left a package
  "installed". Now `gtr.json` changes only after a successful download.
- `install` of GitLab, Bitbucket, git URLs and registry packages silently wrote them
  to `gtr.json` without downloading; now it is an error.
- Saving `gtr.json` dropped every field GTR does not know (`description`, `license`,
  …). Unknown fields are kept; the file is written atomically.
- Package names are validated: `github:../../x` could make `uninstall` delete a
  folder outside the project; archives with `../` paths (zip-slip) are rejected.
- `@1.2.0` was ignored and the default branch was always installed; now the tag
  `v1.2.0` (or `1.2.0`) is used. Ranges fail with a clear message.
- GitHub errors: HTTP status is checked, 404 suggests `GITHUB_TOKEN` for private
  repositories, rate limits and invalid tokens are reported as such; requests time out.
- The download cache lived next to the executable (not writable in Program Files);
  it is now `~/.gtr/cache/downloads` (`GTR_HOME`, ADR-0005).
- `run` with a missing script exited with 0; now it is an error listing the scripts.
  The exit code of a failed script is returned as the exit code of `gtr-manager`.
- `new` removes the folder it created if initialization fails.
- `Makefile` used spaces instead of tabs; `build.ps1`/`win.ps1` left `GOOS`/`GOARCH`
  set in the console and ignored `go build` failures; `build.sh` used a different
  output layout than `build.ps1`.

### Added
- `init -y` / `new <name> -y` — accept defaults without questions.
- `run <script> -- args…` — extra arguments for the script.
- `GITHUB_TOKEN` support.
- Tests for all packages; CI workflow (`ci.workflow.yml`, move it to `.github/workflows/`).

### Changed
- Default scripts: `build` is `go build ./...` (was `go build`).
