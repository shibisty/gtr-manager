package gomodules

import "testing"

func TestHelpers(t *testing.T) {
	if e, _ := Escape("github.com/BurntSushi/toml"); e != "github.com/!burnt!sushi/toml" {
		t.Fatal(e)
	}
	if _, err := Escape("a!b"); err == nil {
		t.Fatal("! must be rejected")
	}
	for _, c := range []struct {
		path, v string
		ok      bool
	}{
		{"github.com/a/b", "v1.2.3", true}, {"github.com/a/b", "v2.0.0", false}, {"github.com/a/b", "v2.0.0+incompatible", true},
		{"github.com/a/b/v2", "v2.1.0", true}, {"github.com/a/b/v2", "v1.0.0", false}, {"gopkg.in/yaml.v3", "v3.0.1", true},
		{"gopkg.in/yaml.v3", "v2.0.0", false}, {"github.com/a/b", "1.2.3", false}, {"github.com/a/b", "v0.0.0-20200823014737-9f7001d12a5f", true},
	} {
		if ValidVersion(c.path, c.v) != c.ok {
			t.Errorf("ValidVersion(%s, %s) != %v", c.path, c.v, c.ok)
		}
	}
	if Compare("v1.10.0", "v1.9.0") <= 0 || Compare("v0.0.0-20200823014737-9f7001d12a5f", "v0.0.1") >= 0 || Compare("v2.0.0+incompatible", "v2.0.0") != 0 {
		t.Fatal("Compare")
	}
	if !IsPseudo("v0.0.0-20200823014737-9f7001d12a5f") || IsPseudo("v1.0.0-rc.1") {
		t.Fatal("IsPseudo")
	}
	for in, want := range map[[2]string]string{{"github.com/a/b", "^1.10"}: "v1.10.0", {"github.com/a/b", "*"}: "v0.0.0", {"github.com/a/b/v3", "*"}: "v3.0.0", {"github.com/a/b/v3", "^3.2"}: "v3.2.0"} {
		if got := Floor(in[0], in[1]); got != want {
			t.Errorf("Floor(%v) = %s, want %s", in, got, want)
		}
	}
	if d, _ := DirName("github.com/A/b", "v1.0.0"); d != "github.com/!a/b@v1.0.0" {
		t.Fatal(d)
	}
	if _, err := DirName("github.com/../x", "v1.0.0"); err == nil {
		t.Fatal("traversal")
	}
	mf := ParseModFile([]byte("module \"x.io/m\"\n\ngo 1.21.3\n\nrequire a.io/b v1.0.0 // indirect\nrequire (\n\tc.io/d v0.1.0\n)\nexclude e.io/f v1.0.0\n"))
	if mf.Module != "x.io/m" || mf.Go != "1.21.3" || mf.Require["a.io/b"] != "v1.0.0" || mf.Require["c.io/d"] != "v0.1.0" || len(mf.Require) != 2 || !mf.Pruned() {
		t.Fatalf("%+v", mf)
	}
	if (ModFile{Go: "1.16"}).Pruned() || (ModFile{}).Pruned() {
		t.Fatal("Pruned")
	}
	if HashMod([]byte("module x\n")) == "" {
		t.Fatal("HashMod")
	}
	db := &SumDB{Private: []string{"*.corp.example", "github.com/me"}}
	if !db.IsPrivate("git.corp.example/x") || !db.IsPrivate("github.com/me/repo") || db.IsPrivate("github.com/meow/x") || !(*SumDB)(nil).IsPrivate("x") {
		t.Fatal("IsPrivate")
	}
}
