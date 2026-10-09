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
		{"x", "github:a/b/../c"}, {"x", "github:a/b#^^1"}, {"x", "gitlab:a/b"}, {"x", "git+https://h/r.git"}, {"x", "banana"}, {"x", "workspace:^^1"}} {
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
}
