package npm

import "testing"

func TestRanges(t *testing.T) {
	cases := []struct {
		rng string
		in  []string
		out []string
	}{
		{"^1.2.3", []string{"1.2.3", "1.9.0"}, []string{"1.2.2", "2.0.0", "2.0.0-rc.1", "1.3.0-beta"}},
		{"^0.2.3", []string{"0.2.3", "0.2.9"}, []string{"0.3.0", "0.2.2"}},
		{"^0.0.3", []string{"0.0.3"}, []string{"0.0.4"}},
		{"^1.2", []string{"1.2.0", "1.5.1"}, []string{"2.0.0", "1.1.9"}},
		{"^0.0", []string{"0.0.7"}, []string{"0.1.0"}},
		{"~1.2.3", []string{"1.2.3", "1.2.9"}, []string{"1.3.0"}},
		{"~1", []string{"1.0.0", "1.9.9"}, []string{"2.0.0"}},
		{"~>1.2", []string{"1.2.5"}, []string{"1.3.0"}},
		{"1.x", []string{"1.0.0", "1.9.0"}, []string{"2.0.0", "0.9.0"}},
		{"1.2.x", []string{"1.2.7"}, []string{"1.3.0"}},
		{"1", []string{"1.4.0"}, []string{"2.0.0"}},
		{"*", []string{"0.0.1", "5.0.0"}, []string{"5.0.0-rc.1"}},
		{"", []string{"1.0.0"}, nil},
		{"latest", []string{"1.0.0"}, nil},
		{">=1.2.3 <2", []string{"1.2.3", "1.99.0"}, []string{"2.0.0", "1.2.2", "2.0.0-0"}},
		{">= 1.2 < 1.4", []string{"1.3.9"}, []string{"1.4.0"}},
		{">1.2", []string{"1.3.0"}, []string{"1.2.9"}},
		{">1.2.3", []string{"1.2.4"}, []string{"1.2.3"}},
		{"<=1.2", []string{"1.2.9"}, []string{"1.3.0"}},
		{"<=1.2.3", []string{"1.2.3"}, []string{"1.2.4"}},
		{"<1.2", []string{"1.1.9"}, []string{"1.2.0", "1.2.0-rc.1"}},
		{"=1.2.3", []string{"1.2.3", "v1.2.3"}, []string{"1.2.4"}},
		{"1.2.3", []string{"1.2.3", "1.2.3+build"}, []string{"1.2.4"}},
		{"1.2.3 - 2.3.4", []string{"1.2.3", "2.3.4"}, []string{"2.3.5"}},
		{"1.2 - 2.3", []string{"1.2.0", "2.3.9"}, []string{"2.4.0"}},
		{"1.2 - *", []string{"9.0.0"}, []string{"1.1.0"}},
		{"^1 || ^3", []string{"1.5.0", "3.0.0"}, []string{"2.0.0"}},
		{"^1.2.3-beta.1", []string{"1.2.3-beta.2", "1.2.3", "1.3.0"}, []string{"1.2.4-beta.1", "1.2.3-alpha"}},
		{">=1.0.0-rc.1", []string{"1.0.0-rc.2", "1.0.0"}, []string{"1.0.0-beta"}},
	}
	for _, c := range cases {
		r, err := ParseRange(c.rng)
		if err != nil {
			t.Errorf("%q: %v", c.rng, err)
			continue
		}
		for _, v := range c.in {
			if pv, err := ParseVersion(v); err != nil || !r.Match(pv) {
				t.Errorf("%q should match %s (%v)", c.rng, v, err)
			}
		}
		for _, v := range c.out {
			if pv, err := ParseVersion(v); err != nil || r.Match(pv) {
				t.Errorf("%q should not match %s (%v)", c.rng, v, err)
			}
		}
	}
	if r, _ := ParseRange("^1"); r.String() != "^1" {
		t.Fatal(r.String())
	}
	for _, bad := range []string{"^a", "1.2.3.4", "a - 1", ">=one", "1.2 - b"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, bad := range []string{"1.2", "1.2.x", "01.2.3", "1.2.3-", "1.2.3-a..b", "a.b.c"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("version %q accepted", bad)
		}
	}
}

func TestCompare(t *testing.T) {
	order := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := 1; i < len(order); i++ {
		a, _ := ParseVersion(order[i-1])
		b, _ := ParseVersion(order[i])
		if Compare(a, b) != -1 || Compare(b, a) != 1 || Compare(a, a) != 0 {
			t.Errorf("%s < %s", order[i-1], order[i])
		}
	}
	a, _ := ParseVersion("1.0.0+x")
	b, _ := ParseVersion("1.0.0+y")
	if Compare(a, b) != 0 || a.String() != "1.0.0+x" {
		t.Fatal("build metadata")
	}
}
