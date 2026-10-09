package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/gomod"
)

// newWorkspace lays out a workspace: a local orm core (shadowing the
// published orm), a driver member, an excluded and a non-package directory.
// The root depends on the published orm-mysql, which needs orm as a peer.
func newWorkspace(t *testing.T) *project {
	gh := universe(t)
	p := newProject(t, gh, `{"name":"ws","version":"0.0.0","private":true,"workspaces":["core","drivers/*","!drivers/skip"],
		"dependencies":{"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1"}}`)
	files := map[string]string{
		"core/gtr.json":           `{"name":"orm","version":"0.1.5","engines":{"go":">=1.22"}}`,
		"core/orm.go":             "package orm\n\nfunc Name() string { return \"local-orm\" }\n",
		"drivers/pg/gtr.json":     `{"name":"orm-pg","version":"0.1.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1"},"devDependencies":{"strutil":"github:shibisty/strutil.go#^1"}}`,
		"drivers/pg/pg.go":        "package pg\n\nimport \"orm\"\n\nfunc Driver() string { return orm.Name() + \"-pg\" }\n",
		"drivers/pg/pg_test.go":   "package pg\n\nimport (\n\t\"strutil\"\n\t\"testing\"\n)\n\nfunc TestDriver(t *testing.T) {\n\tif strutil.Up(Driver()) != \"LOCAL-ORM-PG\" {\n\t\tt.Fatal(Driver())\n\t}\n}\n",
		"drivers/skip/gtr.json":   `{"name":"skipped","version":"0.0.0","dependencies":{"nowhere":"workspace:*"}}`,
		"drivers/notes/README.md": "not a package\n",
		"main.go":                 "package main\n\nimport (\n\t\"fmt\"\n\n\tmysql \"orm-mysql\"\n\tpg \"orm-pg\"\n)\n\nfunc main() { fmt.Println(mysql.Driver(), pg.Driver()) }\n",
	}
	for name, body := range files {
		path := filepath.Join(p.dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(body), 0o644)
	}
	return p
}

func TestWorkspaceInstall(t *testing.T) {
	p := newWorkspace(t)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	l := p.lock(t)
	if _, ok := l.Packages["orm"]; ok || l.Packages["orm-mysql"].Version != "0.1.0" || l.Packages["strutil"].Version != "1.0.0" || len(l.Packages) != 2 {
		t.Fatalf("members must not be locked: %+v", l.Packages)
	}
	if _, err := os.Lstat(filepath.Join(p.dir, "gtr_modules", "orm")); !os.IsNotExist(err) {
		t.Fatal("a member must not be linked into gtr_modules")
	}
	work := p.read("go.work")
	for _, want := range []string{"use (\n\t.\n\t./core\n\t./drivers/pg\n)", "\torm-mysql => ./gtr_modules/orm-mysql\n", "\tstrutil => ./gtr_modules/strutil\n"} {
		if !strings.Contains(work, want) {
			t.Fatalf("go.work lacks %q:\n%s", want, work)
		}
	}
	if strings.Contains(work, "orm =>") || strings.Contains(work, "skip") {
		t.Fatalf("go.work:\n%s", work)
	}
	pgMod := p.read("drivers/pg/go.mod")
	if !strings.Contains(pgMod, "module orm-pg\n") || !strings.Contains(pgMod, "require strutil v1.0.0\n") || strings.Contains(pgMod, "orm v") ||
		strings.Contains(pgMod, "orm-mysql") || strings.Contains(pgMod, "replace") {
		t.Fatalf("member go.mod:\n%s", pgMod)
	}
	if rootMod := p.read("go.mod"); !strings.Contains(rootMod, "require orm-mysql v0.1.0\n") || strings.Contains(rootMod, "replace") {
		t.Fatalf("root go.mod:\n%s", rootMod)
	}
	if !strings.Contains(p.read(".gitignore"), "go.work.sum") || !strings.Contains(p.read("core/.gitignore"), "go.mod") {
		t.Fatal(".gitignore")
	}
	if !strings.Contains(p.log.String(), "Workspace: 2 members (core, drivers/pg)") {
		t.Fatalf("log: %s", p.log)
	}

	root, member, err := FindRoot(filepath.Join(p.dir, "drivers", "pg"))
	if err != nil || root != p.dir || member != "drivers/pg" {
		t.Fatalf("FindRoot: %q %q %v", root, member, err)
	}
	if root, member, _ := FindRoot(filepath.Join(p.dir, "drivers", "skip")); root != filepath.Join(p.dir, "drivers", "skip") || member != "" {
		t.Fatalf("an excluded directory is not a member: %q %q", root, member)
	}

	// The real go command builds and tests the workspace offline.
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	env := append(os.Environ(), "GOWORK="+filepath.Join(p.dir, "go.work"), "GOPROXY=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	for _, c := range []struct{ dir, args string }{{".", "run ."}, {"drivers/pg", "test ./..."}, {".", "vet ./..."}} {
		cmd := exec.Command("go", strings.Fields(c.args)...)
		cmd.Dir, cmd.Env = filepath.Join(p.dir, c.dir), env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in %s: %v\n%s", c.args, c.dir, err, out)
		}
		if c.args == "run ." && strings.TrimSpace(string(out)) != "LOCAL-ORM-MYSQL local-orm-pg" {
			t.Fatalf("output %q", out)
		}
	}
	for _, f := range []string{"go.mod", "go.work", "core/go.mod", "drivers/pg/go.mod"} {
		if st, _ := gomod.Check(filepath.Join(p.dir, filepath.FromSlash(f))); st != gomod.Generated {
			t.Fatalf("go changed %s:\n%s", f, p.read(f))
		}
	}
}

func TestWorkspaceErrors(t *testing.T) {
	write := func(p *project, name, body string) {
		os.WriteFile(filepath.Join(p.dir, filepath.FromSlash(name)), []byte(body), 0o644)
	}
	for _, c := range []struct {
		name string
		edit func(p *project)
		want string
	}{
		{"member version outside a range", func(p *project) {
			write(p, "core/gtr.json", `{"name":"orm","version":"0.2.0"}`)
		}, "requires orm ^0.1"},
		{"workspace: for a non-member", func(p *project) {
			write(p, "drivers/pg/gtr.json", `{"name":"orm-pg","version":"0.1.0","dependencies":{"nothere":"workspace:*"}}`)
		}, "no member of the workspace is called nothere"},
		{"duplicate member names", func(p *project) {
			write(p, "drivers/pg/gtr.json", `{"name":"orm","version":"0.1.0"}`)
		}, `two workspace members are named "orm"`},
		{"hand-written member go.mod", func(p *project) {
			write(p, "core/go.mod", "module github.com/shibisty/orm.go\n")
		}, "core/go.mod was not generated by gtr"},
		{"missing literal member", func(p *project) {
			write(p, "gtr.json", `{"name":"ws","workspaces":["core","nope"]}`)
		}, `directory "nope" not found`},
		{"** globs", func(p *project) {
			write(p, "gtr.json", `{"name":"ws","workspaces":["**"]}`)
		}, `"**" is not supported`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newWorkspace(t)
			c.edit(p)
			if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
	// Outside a workspace, workspace: is an error.
	p := newProject(t, universe(t), `{"name":"app","dependencies":{"orm":"workspace:*"}}`)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "names a workspace member") {
		t.Fatalf("workspace: outside a workspace: %v", err)
	}
	// --force replaces a hand-written member go.mod.
	p = newWorkspace(t)
	write(p, "core/go.mod", "module github.com/shibisty/orm.go\n")
	if err := p.in.Install(ctx, Options{ForceMod: true}); err != nil || !strings.Contains(p.read("core/go.mod"), "module orm\n") {
		t.Fatalf("force: %v", err)
	}
}

func TestWorkspaceLeftAndSync(t *testing.T) {
	p := newWorkspace(t)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := p.in.Sync(ctx, false); err == nil || !strings.Contains(err.Error(), "in a workspace") {
		t.Fatalf("sync: %v", err)
	}
	os.WriteFile(filepath.Join(p.dir, "drivers/pg/go.mod"), []byte(p.read("drivers/pg/go.mod")+"\nrequire x.io/y v1.0.0\n"), 0o644)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "drivers/pg/go.mod was edited by hand") {
		t.Fatalf("edited member go.mod: %v", err)
	}
	if err := p.in.Sync(ctx, true); err != nil || strings.Contains(p.read("drivers/pg/go.mod"), "x.io/y") {
		t.Fatalf("sync --force: %v", err)
	}
	// No longer a workspace: go.work goes away, the member is downloaded again.
	os.WriteFile(filepath.Join(p.dir, "gtr.json"), []byte(`{"name":"ws","dependencies":{"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1","orm":"github:shibisty/orm.go#^0.1"}}`), 0o644)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "go.work")); !os.IsNotExist(err) || p.lock(t).Packages["orm"].Version != "0.1.3" {
		t.Fatalf("go.work left behind or orm not installed: %v %+v", err, p.lock(t).Packages["orm"])
	}
}

func TestWorkspaceAddInMember(t *testing.T) {
	p := newWorkspace(t)
	rootBefore := p.read("gtr.json")
	p.in.Member = "core"
	if err := p.in.Add(ctx, []string{"github:shibisty/strutil.go"}, false); err != nil {
		t.Fatal(err)
	}
	if p.read("gtr.json") != rootBefore || !strings.Contains(p.read("core/gtr.json"), `"strutil": "github:shibisty/strutil.go#^1.0.0"`) {
		t.Fatalf("root:\n%s\ncore:\n%s", p.read("gtr.json"), p.read("core/gtr.json"))
	}
	if !strings.Contains(p.read("core/go.mod"), "require strutil v1.0.0\n") {
		t.Fatalf("core go.mod:\n%s", p.read("core/go.mod"))
	}
	// A member by name becomes workspace:*.
	p.in.Member = "drivers/pg"
	if err := p.in.Add(ctx, []string{"orm"}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read("drivers/pg/gtr.json"), `"orm": "workspace:*"`) {
		t.Fatalf("pg:\n%s", p.read("drivers/pg/gtr.json"))
	}
	p.in.Member = "core"
	if err := p.in.Remove(ctx, []string{"strutil"}); err != nil || strings.Contains(p.read("core/gtr.json"), "strutil") {
		t.Fatalf("remove: %v", err)
	}
	if err := p.in.Add(ctx, []string{"orm"}, false); err == nil || !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Fatalf("self: %v", err)
	}
	// A failed install leaves the member's gtr.json alone.
	before := p.read("core/gtr.json")
	if err := p.in.Add(ctx, []string{"github:shibisty/orm.go-mysql-driver#^9"}, false); err == nil || p.read("core/gtr.json") != before {
		t.Fatalf("failed add: %v", err)
	}
}

// Review regressions: patterns, odd paths, nested non-members, shared file:
// dependencies and former members.
func TestWorkspaceEdgeCases(t *testing.T) {
	p := newWorkspace(t)
	write := func(name, body string) {
		path := filepath.Join(p.dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(body), 0o644)
	}
	// Exclusions apply whatever their position; gtr_modules never matches.
	write("gtr.json", `{"name":"ws","workspaces":["!drivers/skip","*/*","core"],"dependencies":{"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1"}}`)
	// Two members reach the same file: package through different paths.
	write("shared/gtr.json", `{"name":"shared","version":"1.0.0"}`)
	write("shared/s.go", "package shared\n")
	write("core/gtr.json", `{"name":"orm","version":"0.1.5","dependencies":{"shared":"file:../shared"}}`)
	write("drivers/pg/gtr.json", `{"name":"orm-pg","version":"0.1.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1","shared":"file:../../shared"}}`)
	// A member in a directory with a space.
	write("drivers/my driver/gtr.json", `{"name":"orm-my","version":"0.1.0"}`)
	write("drivers/my driver/my.go", "package my\n")
	for i := 0; i < 2; i++ { // the second install sees gtr_modules/* links
		if err := p.in.Install(ctx, Options{}); err != nil {
			t.Fatal(err)
		}
	}
	members, _ := Members(p.dir, p.manifest(t), nil)
	if len(members) != 3 || members["skipped"] != nil || members["orm-mysql"] != nil {
		t.Fatalf("members: %v", memberDirs(members))
	}
	if !strings.Contains(p.read("go.work"), "\t\"./drivers/my driver\"\n") || p.lock(t).Packages["shared"].Source != "file:shared" {
		t.Fatalf("go.work:\n%s\nlock: %+v", p.read("go.work"), p.lock(t).Packages["shared"])
	}
	if _, err := exec.LookPath("go"); err == nil {
		cmd := exec.Command("go", "list", "-m", "all")
		cmd.Dir, cmd.Env = p.dir, append(os.Environ(), "GOWORK="+filepath.Join(p.dir, "go.work"), "GOPROXY=off", "GOFLAGS=", "GOTOOLCHAIN=local")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "orm-my") {
			t.Fatalf("go list: %v\n%s", err, out)
		}
	}
	// A project nested in the workspace (excluded) still finds itself.
	if root, _, err := FindRoot(filepath.Join(p.dir, "drivers", "skip")); err != nil || root != filepath.Join(p.dir, "drivers", "skip") {
		t.Fatalf("excluded: %q %v", root, err)
	}
	// A broken workspace above an unrelated directory does not matter...
	write("tools/gtr.json", `{"name":"tools"}`)
	write("gtr.json", `{"name":"ws","workspaces":["core","missing"]}`)
	if root, _, err := FindRoot(filepath.Join(p.dir, "tools")); err != nil || root != filepath.Join(p.dir, "tools") {
		t.Fatalf("unrelated: %q %v", root, err)
	}
	// ...but does for a member.
	if _, _, err := FindRoot(filepath.Join(p.dir, "core")); err == nil || !strings.Contains(err.Error(), `"missing" not found`) {
		t.Fatalf("member of a broken workspace: %v", err)
	}
	// A former member loses its generated go.mod.
	write("gtr.json", `{"name":"ws","workspaces":["core","drivers/pg"],"dependencies":{"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "drivers", "my driver", "go.mod")); !os.IsNotExist(err) {
		t.Fatalf("former member go.mod: %v", err)
	}
}

func TestWorkspaceRootWithBrackets(t *testing.T) {
	p := newWorkspace(t)
	odd := filepath.Join(filepath.Dir(p.dir), "proj [old]")
	if err := os.Rename(p.dir, odd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(odd) })
	root, member, err := FindRoot(filepath.Join(odd, "core"))
	if err != nil || root != odd || member != "core" {
		t.Fatalf("%q %q %v", root, member, err)
	}
}

// A member used as a file: package by another project keeps the go.mod its
// workspace generated: a package go.mod would require the other members at
// a placeholder version, which breaks the workspace.
func TestMemberAsFilePackageKeepsGoMod(t *testing.T) {
	p := newWorkspace(t)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	before := p.read("drivers/pg/go.mod")
	app := newProject(t, p.gh, `{"name":"app","dependencies":{"orm-pg":"file:`+filepath.ToSlash(filepath.Join(p.dir, "drivers", "pg"))+`","orm":"file:`+filepath.ToSlash(filepath.Join(p.dir, "core"))+`"}}`)
	if err := app.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if p.read("drivers/pg/go.mod") != before {
		t.Fatalf("the member go.mod was rewritten:\n%s", p.read("drivers/pg/go.mod"))
	}
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
}
