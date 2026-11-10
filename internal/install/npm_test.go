package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/npm"
	"gtr-manager/internal/resolve"
	"gtr-manager/internal/spec"
)

// npmRegistry serves name@version packages (no dependencies of their own
// unless given).
func npmRegistry(t *testing.T, pkgs map[string][]string, deps map[string]map[string]string) *httptest.Server {
	t.Helper()
	tarballs := map[string][]byte{}
	docs := map[string]map[string]any{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if data, ok := tarballs[r.URL.Path]; ok {
			w.Write(data)
			return
		}
		doc, ok := docs[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	for name, versions := range pkgs {
		vs := map[string]any{}
		for _, v := range versions {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for f, body := range map[string]string{"package/package.json": `{"name":"` + name + `","version":"` + v + `"}`, "package/index.js": "// " + name + " " + v} {
				tw.WriteHeader(&tar.Header{Name: f, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
				tw.Write([]byte(body))
			}
			tw.Close()
			gz.Close()
			p := "/-/" + strings.ReplaceAll(name, "/", "_") + "-" + v + ".tgz"
			tarballs[p] = buf.Bytes()
			sum := sha512.Sum512(buf.Bytes())
			vs[v] = map[string]any{"name": name, "version": v, "dependencies": deps[name+"@"+v],
				"dist": map[string]any{"tarball": srv.URL + p, "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])}}
		}
		docs[name] = map[string]any{"name": name, "versions": vs, "dist-tags": map[string]string{"latest": versions[len(versions)-1]}}
	}
	return srv
}

func TestNPMDependencies(t *testing.T) {
	gh := universe(t)
	gh.Commit("shibisty/ui.go", map[string]string{
		"gtr.json": gtrJSON("ui", map[string]string{"preact": "npm:^10.5", "@preact/signals": "npm:^1"}, nil),
		"ui.go":    "package ui\n",
	}, "v0.1.0")
	reg := npmRegistry(t, map[string][]string{
		"preact":          {"10.4.0", "10.19.3", "11.0.0-beta.1"},
		"@preact/signals": {"1.2.0"},
		"htm":             {"3.1.1"},
	}, map[string]map[string]string{"@preact/signals@1.2.0": {"preact": "^10"}})
	p := newProject(t, gh, `{"name":"app","engines":{"go":">=1.22"},"dependencies":{"ui":"github:shibisty/ui.go#^0.1","htm":"npm:^3"}}`)
	p.in.NPM = &npm.Registry{URL: reg.URL}
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err, p.log)
	}
	for path, want := range map[string]string{"node_modules/preact": "10.19.3", "node_modules/@preact/signals": "1.2.0", "node_modules/htm": "3.1.1"} {
		if data := p.read(path + "/index.js"); !strings.Contains(data, want) {
			t.Errorf("%s: %q", path, data)
		}
	}
	l := p.lock(t)
	if e := l.NPM["node_modules/preact"]; e.Version != "10.19.3" || e.Source != "npm:preact" || !strings.HasPrefix(e.Integrity, "sha512-") || e.Tarball == "" {
		t.Fatalf("lock: %+v", l.NPM)
	}
	if _, ok := l.Packages["preact"]; ok {
		t.Fatal("npm package among gtr packages")
	}
	if strings.Contains(p.read("go.mod"), "preact") || !strings.Contains(p.read(".gitignore"), "node_modules/") {
		t.Fatalf("go.mod:\n%s\n.gitignore:\n%s", p.read("go.mod"), p.read(".gitignore"))
	}
	if !strings.Contains(p.log.String(), "3 npm packages in node_modules") {
		t.Fatal(p.log)
	}

	// gtr ci installs the lock exactly, offline from the store too.
	os.RemoveAll(filepath.Join(p.dir, "node_modules"))
	p.in.NPM = &npm.Registry{URL: "http://127.0.0.1:1"} // the registry is not needed
	if err := p.in.Install(ctx, Options{Frozen: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read("node_modules/htm/index.js"), "3.1.1") {
		t.Fatal("frozen install")
	}
	// A range the lock no longer satisfies: gtr ci refuses.
	m := p.manifest(t)
	m.Dependencies["htm"] = "npm:^4"
	writeManifest(t, p, m)
	if err := p.in.Install(ctx, Options{Frozen: true}); err == nil || !strings.Contains(err.Error(), "gtr.lock does not match gtr.json for npm package htm") {
		t.Fatal(err)
	}
	// Removing npm dependencies removes node_modules entries and the lock section.
	p.in.NPM = &npm.Registry{URL: reg.URL}
	delete(m.Dependencies, "htm")
	delete(m.Dependencies, "ui")
	writeManifest(t, p, m)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "node_modules/preact")); !os.IsNotExist(err) {
		t.Fatal("node_modules/preact kept")
	}
	if l := p.lock(t); len(l.NPM) != 0 {
		t.Fatal(l.NPM)
	}

	// Bad npm entries are reported.
	m.Dependencies["Bad"] = "npm:^1"
	writeManifest(t, p, m)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "invalid npm package name") {
		t.Fatal(err)
	}
	delete(m.Dependencies, "Bad")
	m.Dependencies["htm"] = "npm:>=one"
	writeManifest(t, p, m)
	if err := p.in.Install(ctx, Options{}); err == nil || !strings.Contains(err.Error(), "invalid npm version range") {
		t.Fatal(err)
	}
}

func TestAddNPM(t *testing.T) {
	reg := npmRegistry(t, map[string][]string{"preact": {"10.19.3"}, "@preact/signals": {"1.2.0"}}, nil)
	p := newProject(t, universe(t), `{"name":"app","engines":{"go":">=1.22"}}`)
	p.in.NPM = &npm.Registry{URL: reg.URL}
	if err := p.in.Add(ctx, []string{"npm:preact", "npm:@preact/signals@~1.2"}, false); err != nil {
		t.Fatal(err, p.log)
	}
	m := p.manifest(t)
	if m.Dependencies["preact"] != "npm:^10.19.3" || m.Dependencies["@preact/signals"] != "npm:~1.2" {
		t.Fatal(m.Dependencies)
	}
	if !strings.Contains(p.read("node_modules/@preact/signals/index.js"), "1.2.0") {
		t.Fatal("not installed")
	}
	for arg, want := range map[string]string{"npm:Bad": "invalid npm package name", "npm:preact@>=one": "invalid range", "npm:nope": "not found"} {
		if err := p.in.Add(ctx, []string{arg}, false); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", arg, err)
		}
	}
	reg2 := npmRegistry(t, map[string][]string{"pre": {"1.0.0-rc.1"}}, nil)
	p.in.NPM = &npm.Registry{URL: reg2.URL}
	if err := p.in.Add(ctx, []string{"npm:pre"}, false); err == nil || !strings.Contains(err.Error(), "no stable version") {
		t.Fatal(err)
	}
}

func writeManifest(t *testing.T, p *project, m *manifest.Manifest) {
	t.Helper()
	if err := manifest.Save(filepath.Join(p.dir, "gtr.json"), m); err != nil {
		t.Fatal(err)
	}
}

func TestNPMDepsMerge(t *testing.T) {
	sp := func(r string) spec.Spec { return spec.Spec{Kind: spec.NPM, Range: r} }
	root := map[string]spec.Spec{"preact": sp("^10.5"), "orm": {Kind: spec.GitHub, Range: "^0.1"}}
	nodes := map[string]*resolve.Node{
		"a": {NPMDeps: map[string]spec.Spec{"preact": sp("^10"), "htm": sp("^3"), "x": sp("*"), "y": sp("^1"), "z": sp("^1 || ^2")}},
		"b": {NPMDeps: map[string]spec.Spec{"htm": sp(">=3.1"), "x": sp("^2"), "y": sp("*"), "z": sp("^2")}},
	}
	got := npmDeps(root, nodes)
	want := map[string]string{"preact": "^10.5", "htm": "^3 >=3.1", "x": "^2", "y": "^1", "z": "^1 || ^2"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if fromLock("node_modules/x", lock.Entry{Version: "1.0.0"}).Name != "x" ||
		fromLock("node_modules/a/node_modules/@s/y", lock.Entry{Version: "1.0.0", Source: "npm:@s/y", Optional: []string{"o"}}).Optional["o"] != true {
		t.Fatal("fromLock")
	}
	e := toLock(&npm.Package{Name: "p", Version: "1.0.0", Optional: map[string]bool{"b": true, "a": true}})
	if strings.Join(e.Optional, ",") != "a,b" || e.Dependencies != nil {
		t.Fatalf("%+v", e)
	}
	if plural(1, "x", "xs") != "x" || plural(2, "x", "xs") != "xs" {
		t.Fatal("plural")
	}
	in := &Installer{}
	t.Setenv("GTR_NPM_REGISTRY", "https://npm.example")
	if in.npmRegistry().URL != "https://npm.example" || in.npmRegistry() != in.NPM {
		t.Fatal("npmRegistry")
	}
}

// In a workspace node_modules is installed at the root, so the root's
// .gitignore lists it, not the member's.
func TestNPMInWorkspaceMember(t *testing.T) {
	p := newWorkspace(t)
	p.in.NPM = &npm.Registry{URL: npmRegistry(t, map[string][]string{"preact": {"10.19.3"}}, nil).URL}
	p.in.Member = "core"
	if err := p.in.Add(ctx, []string{"npm:preact"}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read("core/gtr.json"), `"preact": "npm:^10.19.3"`) {
		t.Fatal(p.read("core/gtr.json"))
	}
	if _, err := os.Stat(filepath.Join(p.dir, "node_modules", "preact", "package.json")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.read(".gitignore"), "node_modules/") || strings.Contains(p.read("core/.gitignore"), "node_modules") {
		t.Fatalf("root:\n%s\ncore:\n%s", p.read(".gitignore"), p.read("core/.gitignore"))
	}
}
