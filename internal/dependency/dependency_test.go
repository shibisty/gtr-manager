package dependency

import "testing"

func TestParse(t *testing.T) {
	ok := map[string]Dependency{
		"github:shibisty/orm.go":        {"github", "shibisty/orm.go", "*"},
		" github:a/b@1.2.0 ":            {"github", "a/b", "1.2.0"},
		"github:a/b@^1":                 {"github", "a/b", "^1"},
		"gitlab:group/proj@v2.0.0-rc.1": {"gitlab", "group/proj", "v2.0.0-rc.1"},
		"bitbucket:a/b":                 {"bitbucket", "a/b", "*"},
		"orm":                           {"gtr", "orm", "*"},
		"repo/router@>=1.0 <2":          {"gtr", "repo/router", ">=1.0 <2"},
		"https://git.example.com/x.git": {"git", "https://git.example.com/x.git", "*"},
		"https://user@host/x.git":       {"git", "https://user@host/x.git", "*"},
	}
	for in, want := range ok {
		d, err := Parse(in)
		if err != nil || *d != want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", in, d, err, want)
		}
	}
	for _, in := range []string{"", "@1", "a@", "github:", "github:a", "github:../x", "github:a/..", "github:a/b/c",
		"github:a/b c", "../evil", "a/../b", "/abs", `C:\x`, "a@1;rm", "-flag"} {
		if d, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %+v, want error", in, d)
		}
	}
}

func TestKeyAndString(t *testing.T) {
	for in, want := range map[string][2]string{
		"github:a/b@1.0.0": {"github:a/b", "github:a/b@1.0.0"},
		"github:a/b":       {"github:a/b", "github:a/b"},
		"orm@^1":           {"orm", "orm@^1"},
	} {
		d, _ := Parse(in)
		if d.Key() != want[0] || d.String() != want[1] {
			t.Errorf("%s: %s %s", in, d.Key(), d.String())
		}
	}
}
