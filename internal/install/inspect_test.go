package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gtr-manager/internal/lock"
	"gtr-manager/internal/manifest"
	"gtr-manager/internal/source"
)

func inspected(t *testing.T) *project {
	t.Helper()
	gh := universe(t)
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","engines":{"go":">=1.22"},
		"dependencies":{"orm":"github:shibisty/orm.go#^0.1","orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1","example.com/top":"^1.0.0"}}`)
	fakeModules(t, p, map[string]map[string]string{
		"example.com/top": {"v1.0.0": "module example.com/top\n\ngo 1.21\n\nrequire example.com/low v1.1.0\n"},
		"example.com/low": {"v1.1.0": "module example.com/low\n\ngo 1.21\n", "v1.4.0": "module example.com/low\n\ngo 1.21\n"},
	})
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTree(t *testing.T) {
	p := inspected(t)
	st, err := p.in.State()
	if err != nil {
		t.Fatal(err)
	}
	trees := st.Tree(0)
	if len(trees) != 1 || trees[0].Name != "app" || len(trees[0].Children) != 3 {
		t.Fatalf("roots: %+v", trees)
	}
	var names []string
	for _, c := range trees[0].Children {
		names = append(names, c.Name+"@"+c.Version)
		if len(c.Children) != 0 {
			t.Fatalf("depth 0 expanded %s", c.Name)
		}
	}
	if strings.Join(names, " ") != "example.com/top@v1.0.0 orm@0.1.3 orm-mysql@0.1.0" {
		t.Fatalf("direct: %v", names)
	}
	full := st.Tree(-1)[0]
	top, mysql := full.Children[0], full.Children[2]
	if len(top.Children) != 1 || top.Children[0].Name != "example.com/low" || top.Children[0].Version != "v1.1.0" {
		t.Fatalf("top: %+v", top.Children)
	}
	if len(mysql.Children) != 1 || mysql.Children[0].Name != "strutil" || mysql.Children[0].Source != "github:shibisty/strutil.go" {
		t.Fatalf("orm-mysql: %+v", mysql.Children)
	}
}

func TestTreeDedupAndMissing(t *testing.T) {
	gh := universe(t)
	gh.Commit("shibisty/other.go", map[string]string{
		"gtr.json": gtrJSON("other", map[string]string{"strutil": "github:shibisty/strutil.go#^1"}, nil),
		"o.go":     "package other",
	}, "v0.1.0")
	p := newProject(t, gh, `{"name":"app","version":"1.0.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1",
		"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1","other":"github:shibisty/other.go#^0.1"}}`)
	if err := p.in.Install(ctx, Options{}); err != nil {
		t.Fatal(err)
	}
	// A dependency added to gtr.json but not installed yet.
	os.WriteFile(filepath.Join(p.dir, "gtr.json"), []byte(`{"name":"app","version":"1.0.0","dependencies":{"orm":"github:shibisty/orm.go#^0.1",
		"orm-mysql":"github:shibisty/orm.go-mysql-driver#^0.1","other":"github:shibisty/other.go#^0.1","faker":"github:x/faker.go"}}`), 0o644)
	st, err := p.in.State()
	if err != nil {
		t.Fatal(err)
	}
	kids := st.Tree(-1)[0].Children
	if !kids[0].Missing || kids[0].Name != "faker" {
		t.Fatalf("missing: %+v", kids[0])
	}
	mysql, other := kids[2], kids[3]
	if mysql.Children[0].Deduped || !other.Children[0].Deduped && len(other.Children[0].Children) != 0 {
		t.Fatalf("dedup: %+v %+v", mysql.Children[0], other.Children[0])
	}
}

func TestWhy(t *testing.T) {
	p := inspected(t)
	st, err := p.in.State()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := st.Why("strutil", 10)
	if err != nil || len(paths) != 1 {
		t.Fatalf("%v %v", paths, err)
	}
	if l := paths[0]; len(l) != 2 || l[0].From != "app" || l[0].To != "orm-mysql" || l[1].Spec != "github:shibisty/strutil.go#^1" {
		t.Fatalf("strutil: %+v", paths[0])
	}
	paths, _ = st.Why("orm", 10)
	if len(paths) != 2 || len(paths[0]) != 1 || paths[0][0].Spec != "github:shibisty/orm.go#^0.1" || !paths[1][1].Peer || paths[1][1].Spec != "^0.1" {
		t.Fatalf("orm: %+v", paths)
	}
	paths, _ = st.Why("example.com/low", 10)
	if len(paths) != 1 || paths[0][1].Spec != "v1.1.0" {
		t.Fatalf("low: %+v", paths)
	}
	if _, err := st.Why("nope", 10); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestOutdated(t *testing.T) {
	p := inspected(t)
	p.gh.Commit("shibisty/orm.go", map[string]string{"gtr.json": gtrJSON("orm", nil, nil), "orm.go": "package orm"}, "v0.1.5")
	p.in.GitHub = &source.GitHub{API: p.gh.URL, Client: p.gh.Client(), Tmp: p.home.Tmp()} // a new run: tags not cached
	st, err := p.in.State()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.in.Outdated(ctx, st, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "orm" || rows[0].Current != "0.1.3" || rows[0].Wanted != "0.1.5" || rows[0].Latest != "0.2.0" {
		t.Fatalf("direct: %+v", rows)
	}
	rows, err = p.in.Outdated(ctx, st, true)
	if err != nil {
		t.Fatal(err)
	}
	var low *Outdated
	for i := range rows {
		if rows[i].Name == "example.com/low" {
			low = &rows[i]
		}
	}
	if len(rows) != 2 || low == nil || low.Current != "v1.1.0" || low.Wanted != "" || low.Latest != "v1.4.0" || low.Direct {
		t.Fatalf("all: %+v", rows)
	}
	// After `gtr update`, orm is at the wanted version.
	if err := p.in.Install(ctx, Options{Update: map[string]bool{"orm": true}}); err != nil {
		t.Fatal(err)
	}
	st, _ = p.in.State()
	rows, _ = p.in.Outdated(ctx, st, false)
	if len(rows) != 1 || rows[0].Current != "0.1.5" || rows[0].Wanted != "0.1.5" {
		t.Fatalf("after update: %+v", rows)
	}
}

func TestStateNeedsLock(t *testing.T) {
	p := newProject(t, universe(t), `{"name":"app","version":"1.0.0"}`)
	if _, err := p.in.State(); err == nil || !strings.Contains(err.Error(), "gtr install") {
		t.Fatalf("no lock: %v", err)
	}
}

// Dense module graphs (golang.org/x/... require each other) must not make
// gtr why walk every path.
func TestWhyDenseGraph(t *testing.T) {
	l := &lock.Lock{Packages: map[string]lock.Entry{}}
	var clique []string
	for i := 0; i < 14; i++ {
		clique = append(clique, fmt.Sprintf("example.com/m%02d", i))
	}
	for _, m := range clique {
		deps := map[string]string{}
		for _, o := range clique {
			if o != m {
				deps[o] = "v1.0.0"
			}
		}
		l.Packages[m] = lock.Entry{Version: "v1.0.0", Source: "go", Dependencies: deps}
	}
	l.Packages["zz"] = lock.Entry{Version: "1.0.0", Source: "github:o/zz", Dependencies: map[string]string{"example.com/target": "v1.0.0"}}
	l.Packages["example.com/target"] = lock.Entry{Version: "v1.0.0", Source: "go"}
	m := &manifest.Manifest{Name: "app", Dependencies: map[string]string{"example.com/m00": "^1", "zz": "github:o/zz"}}
	st := &State{Manifest: m, Lock: l, dir: t.TempDir()}
	done := make(chan [][]Link)
	go func() {
		paths, _ := st.Why("example.com/target", 5)
		done <- paths
	}()
	select {
	case paths := <-done:
		if len(paths) == 0 || len(paths[0]) != 2 {
			t.Fatalf("paths: %v", paths)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gtr why did not finish")
	}
}
