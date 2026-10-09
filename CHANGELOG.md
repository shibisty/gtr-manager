# Changelog

## Unreleased

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
