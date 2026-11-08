package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"gtr-manager/internal/source/githubtest"
)

func TestListWhyOutdated(t *testing.T) {
	gh := githubtest.New(t)
	gh.Commit("o/strutil.go", map[string]string{"gtr.json": `{"name":"strutil","version":"0"}`, "s.go": "package strutil"}, "v1.0.0")
	gh.Commit("o/web.go", map[string]string{"gtr.json": `{"name":"web","version":"0","dependencies":{"strutil":"github:o/strutil.go#^1"}}`, "w.go": "package web"}, "v0.1.0")
	gh.Commit("o/testkit.go", map[string]string{"gtr.json": `{"name":"testkit","version":"0"}`, "t.go": "package testkit"}, "v0.3.0")
	useGitHub(t, gh)
	out := project(t, `{"name":"app","version":"1.0.0"}`)

	if err := List(nil); err == nil || !strings.Contains(err.Error(), "gtr install") {
		t.Fatalf("before install: %v", err)
	}
	if err := Add([]string{"github:o/web.go"}); err != nil {
		t.Fatal(err)
	}
	if err := Add([]string{"-D", "github:o/testkit.go"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := List(nil); err != nil {
		t.Fatal(err)
	}
	want := "app@1.0.0\n├── web@0.1.0  github:o/web.go\n└── testkit@0.3.0  github:o/testkit.go  (dev)\n"
	if out.String() != want {
		t.Fatalf("list:\n%s\nwant:\n%s", out, want)
	}
	out.Reset()
	if err := List([]string{"--tree"}); err != nil || !strings.Contains(out.String(), "│   └── strutil@1.0.0  github:o/strutil.go\n") {
		t.Fatalf("tree: %v\n%s", err, out)
	}
	out.Reset()
	if err := List([]string{"--depth=1", "--json"}); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(out.Bytes(), &nodes); err != nil || nodes[0]["name"] != "app" {
		t.Fatalf("json: %v %s", err, out)
	}

	out.Reset()
	if err := Why([]string{"strutil"}); err != nil {
		t.Fatal(err)
	}
	if want := "strutil@1.0.0  github:o/strutil.go\n  app → web@0.1.0 → strutil  (requires \"github:o/strutil.go#^1\")\n"; out.String() != want {
		t.Fatalf("why:\n%s\nwant:\n%s", out, want)
	}

	gh.Commit("o/testkit.go", map[string]string{"gtr.json": `{"name":"testkit","version":"0"}`, "t.go": "package testkit"}, "v0.4.0")
	useGitHub(t, gh) // a new run: tags are listed again
	out.Reset()
	if err := Outdated(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Package") || !strings.Contains(out.String(), "testkit (dev)  0.3.0    0.3.0   0.4.0   github:o/testkit.go") {
		t.Fatalf("outdated:\n%s", out)
	}

	for name, err := range map[string]error{
		"list arg":     List([]string{"x"}),
		"list depth":   List([]string{"--depth", "-1"}),
		"list depth2":  List([]string{"--depth"}),
		"why none":     Why(nil),
		"why unknown":  Why([]string{"nope"}),
		"outdated arg": Outdated([]string{"x"}),
	} {
		if err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

func TestOfflineFlag(t *testing.T) {
	gh := githubtest.New(t)
	gh.Commit("o/strutil.go", map[string]string{"gtr.json": `{"name":"strutil","version":"0"}`, "s.go": "package strutil"}, "v1.0.0")
	gh.Commit("o/other.go", map[string]string{"gtr.json": `{"name":"other","version":"0"}`, "o.go": "package other"}, "v1.0.0")
	useGitHub(t, gh)
	project(t, `{"name":"app","version":"1.0.0"}`)
	if err := Add([]string{"github:o/strutil.go"}); err != nil {
		t.Fatal(err)
	}
	downloads := gh.Zipball
	for name, err := range map[string]error{
		"install": Install([]string{"--offline"}), "ci": CI([]string{"--offline"}), "update": Update([]string{"--offline"}),
	} {
		if err != nil {
			t.Fatalf("%s --offline: %v", name, err)
		}
	}
	if err := Add([]string{"--offline", "github:o/other.go"}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("add --offline of a new source: %v", err)
	}
	t.Setenv("GTR_OFFLINE", "1")
	if err := Add([]string{"github:o/other.go"}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("GTR_OFFLINE=1: %v", err)
	}
	if gh.Zipball != downloads {
		t.Fatal("offline commands downloaded")
	}
}
