package spec

import "testing"

func TestParse(t *testing.T) {
	ok := map[[2]string]Spec{
		{"orm", "github:shibisty/orm.go#^0.1"}:                {Kind: GitHub, Repo: "shibisty/orm.go", Range: "^0.1"},
		{"orm-mysql", "github:shibisty/orm.go/drivers/mysql"}: {Kind: GitHub, Repo: "shibisty/orm.go", Subdir: "drivers/mysql", Range: "*"},
		{"passport", "file:../passport.go"}:                   {Kind: File, Path: "../passport.go", Range: "*"},
		{"faker", "^0.1"}:                                     {Kind: Registry, Range: "^0.1"},
		{"github.com/go-sql-driver/mysql", "^1.10"}:           {Kind: GoModule, Range: "^1.10"},
		{"orm", "workspace:*"}:                                {Kind: Workspace, Range: "*"},
		{"orm", "workspace:^"}:                                {Kind: Workspace, Range: "*"},
		{"orm", "workspace:^0.1"}:                             {Kind: Workspace, Range: "^0.1"},
		{"x", ">=1.0 <2.0"}:                                   {Kind: Registry, Range: ">=1.0 <2.0"},
		{"orm", "gitlab:acme/orm#^0.2"}:                       {Kind: GitLab, Repo: "acme/orm", Range: "^0.2"},
		{"orm", "gitlab:acme/libs/orm//drivers/pg"}:           {Kind: GitLab, Repo: "acme/libs/orm", Subdir: "drivers/pg", Range: "*"},
		{"orm", "git+https://git.example.com/a/orm.git#~1.2"}: {Kind: Git, URL: "https://git.example.com/a/orm.git", Range: "~1.2"},
		{"orm", "git+ssh://git@host:2222/orm.git//core"}:      {Kind: Git, URL: "ssh://git@host:2222/orm.git", Subdir: "core", Range: "*"},
		{"orm", "git+file:///srv/git/orm//pkg/orm#^1"}:        {Kind: Git, URL: "file:///srv/git/orm", Subdir: "pkg/orm", Range: "^1"},
	}
	for in, want := range ok {
		got, err := Parse(in[0], in[1])
		got.Raw = ""
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, got, err, want)
		}
		if err == nil {
			back, _ := Parse(in[0], got.String())
			back.Raw = ""
			if back != want {
				t.Errorf("round trip %q → %q", in[1], got.String())
			}
		}
	}
	for _, bad := range [][2]string{{"x", ""}, {"x", "file:"}, {"x", "github:"}, {"x", "github:onlyowner"},
		{"x", "github:a/b/../c"}, {"x", "github:a/b#^^1"}, {"x", "gitlab:a"}, {"x", "gitlab:a/../b"}, {"x", "gitlab:a/b//../c"},
		{"x", "git+http://h/r.git"}, {"x", "git+ftp://h/r"}, {"x", "git+https://"}, {"x", "git+https:/h"}, {"x", "git+https://h/r.git#^^1"}, {"x", "banana"}, {"x", "workspace:^^1"}} {
		if s, err := Parse(bad[0], bad[1]); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", bad, s)
		}
	}
}

func TestKeys(t *testing.T) {
	for _, k := range []string{"orm", "orm-mysql", "passport-session-redis", "github.com/go-sql-driver/mysql", "gopkg.in/yaml.v3"} {
		if err := ValidateKey(k); err != nil {
			t.Errorf("%s: %v", k, err)
		}
	}
	for _, k := range []string{"o", "Orm", "orm--x", "-orm", "orm_x", "1orm", "gtr", "main", "self", "Github.com/x", "github.com/a b"} {
		if err := ValidateKey(k); err == nil {
			t.Errorf("%s must be invalid", k)
		}
	}
	if !IsGoModule("github.com/x/y") || IsGoModule("orm/schema") || !IsLegacyKey("github:a/b") || IsLegacyKey("orm") {
		t.Fatal("IsGoModule/IsLegacyKey")
	}
	a, _ := Parse("x", "github:a/b#^1")
	b, _ := Parse("x", "github:a/b#^2")
	c, _ := Parse("x", "github:a/c")
	if !a.SameSource(b) || a.SameSource(c) {
		t.Fatal("SameSource")
	}
	g1, _ := Parse("x", "git+https://h/a.git#^1")
	g2, _ := Parse("x", "git+https://h/b.git#^1")
	if g1.SameSource(g2) || !g1.IsGit() || c.IsGit() == false || (Spec{Kind: File}).IsGit() {
		t.Fatal("git SameSource/IsGit")
	}
}

func TestNPM(t *testing.T) {
	s, err := Parse("preact", "npm:^10.19")
	if err != nil || s.Kind != NPM || s.Range != "^10.19" || s.String() != "npm:^10.19" {
		t.Fatal(s, err)
	}
	if s, _ := Parse("preact", "npm:"); s.Range != "*" || s.String() != "npm:*" {
		t.Fatal(s)
	}
	if _, err := Parse("preact", "npm:>=one"); err == nil {
		t.Fatal("bad range")
	}
	if ValidateDep("@preact/signals", "npm:^1") != nil || ValidateDep("@preact/signals", "^1") == nil || ValidateDep("orm", "github:a/b") != nil {
		t.Fatal("ValidateDep")
	}
}
