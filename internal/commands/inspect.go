package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"gtr-manager/internal/install"
)

// List prints the installed packages from gtr.lock (no network):
//
//	gtr list               direct dependencies of the project (and members)
//	gtr list --tree        the whole tree
//	gtr list --depth 2     two levels
//	gtr list --json
func List(args []string) error {
	depth, asJSON := 0, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--tree" || a == "--all" || a == "-a":
			depth = -1
		case a == "--json":
			asJSON = true
		case a == "--depth" || strings.HasPrefix(a, "--depth="):
			v, ok := strings.CutPrefix(a, "--depth=")
			if !ok {
				if i+1 >= len(args) {
					return fmt.Errorf("--depth needs a number")
				}
				i++
				v = args[i]
			}
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return fmt.Errorf("--depth: %q is not a number", v)
			}
			depth = n
		default:
			return fmt.Errorf("unexpected argument %q; usage: gtr list [--tree | --depth N] [--json]", a)
		}
	}
	in, err := installer()
	if err != nil {
		return err
	}
	st, err := in.State()
	if err != nil {
		return err
	}
	trees := st.Tree(depth)
	if asJSON {
		data, _ := json.MarshalIndent(trees, "", "  ")
		fmt.Fprintln(Stdout, string(data))
		return nil
	}
	for i, root := range trees {
		if i > 0 {
			fmt.Fprintln(Stdout)
		}
		label := root.Name
		if root.Version != "" {
			label += "@" + root.Version
		}
		if dir, ok := strings.CutPrefix(root.Source, "workspace:"); ok {
			label += "  " + dir
		}
		fmt.Fprintln(Stdout, label)
		if len(root.Children) == 0 {
			fmt.Fprintln(Stdout, "└── (no dependencies)")
		}
		printChildren(Stdout, root.Children, "")
	}
	return nil
}

func printChildren(w io.Writer, nodes []*install.Node, prefix string) {
	for i, n := range nodes {
		branch, next := "├── ", "│   "
		if i == len(nodes)-1 {
			branch, next = "└── ", "    "
		}
		fmt.Fprintln(w, prefix+branch+nodeLabel(n))
		printChildren(w, n.Children, prefix+next)
	}
}

func nodeLabel(n *install.Node) string {
	if n.Missing {
		return n.Name + "  (not installed: run `gtr install`)"
	}
	s := n.Name + "@" + n.Version
	if n.Source != "" && n.Source != "go" {
		s += "  " + n.Source
	}
	if n.Dev {
		s += "  (dev)"
	}
	if n.Deduped {
		s += "  (deduped)"
	}
	return s
}

// Why prints the chains of requirements that bring a package in.
func Why(args []string) error {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: gtr why <package>")
	}
	name := args[0]
	in, err := installer()
	if err != nil {
		return err
	}
	st, err := in.State()
	if err != nil {
		return err
	}
	const limit = 20
	paths, err := st.Why(name, limit)
	if err != nil {
		return err
	}
	head := name
	if e, ok := st.Lock.Packages[name]; ok {
		head += "@" + e.Version
		if e.Source != "go" {
			head += "  " + e.Source
		}
	} else if mem := st.Members[name]; mem != nil {
		head += "@" + mem.Version + "  workspace member " + mem.Dir
	}
	fmt.Fprintln(Stdout, head)
	if len(paths) == 0 {
		fmt.Fprintln(Stdout, "  nothing requires it (a workspace member)")
		return nil
	}
	versions := func(n string) string {
		if e, ok := st.Lock.Packages[n]; ok {
			return n + "@" + e.Version
		}
		if mem := st.Members[n]; mem != nil {
			return n + "@" + mem.Version
		}
		return n
	}
	for _, p := range paths {
		parts := []string{p[0].From}
		for _, l := range p[:len(p)-1] {
			parts = append(parts, versions(l.To))
		}
		last := p[len(p)-1]
		kind := "requires"
		if last.Peer {
			kind = "peer"
		}
		fmt.Fprintf(Stdout, "  %s → %s  (%s %q)\n", strings.Join(parts, " → "), name, kind, last.Spec)
	}
	if len(paths) == limit {
		fmt.Fprintf(Stdout, "  … (first %d chains)\n", limit)
	}
	return nil
}

// Outdated lists packages that have newer versions: "wanted" is the newest
// version the ranges in gtr.json files (and in packages) allow — what `gtr
// update` installs — and "latest" the newest overall.
func Outdated(args []string) error {
	all, asJSON := false, false
	for _, a := range args {
		switch a {
		case "--all", "-a":
			all = true
		case "--json":
			asJSON = true
		default:
			return fmt.Errorf("unexpected argument %q; usage: gtr outdated [--all] [--json]", a)
		}
	}
	in, err := installer()
	if err != nil {
		return err
	}
	st, err := in.State()
	if err != nil {
		return err
	}
	rows, err := in.Outdated(context.Background(), st, all)
	if err != nil {
		return err
	}
	if asJSON {
		type jrow struct {
			Name    string `json:"name"`
			Current string `json:"current"`
			Wanted  string `json:"wanted,omitempty"`
			Latest  string `json:"latest"`
			Source  string `json:"source"`
			Dev     bool   `json:"dev,omitempty"`
		}
		out := []jrow{}
		for _, r := range rows {
			out = append(out, jrow{r.Name, r.Current, r.Wanted, r.Latest, r.Source, r.Dev})
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(Stdout, string(data))
		return nil
	}
	if len(rows) == 0 {
		fmt.Fprintln(Stdout, "All packages are up to date")
		return nil
	}
	tw := tabwriter.NewWriter(Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Package\tCurrent\tWanted\tLatest\tSource")
	for _, r := range rows {
		name := r.Name
		if r.Dev {
			name += " (dev)"
		}
		if !r.Direct {
			name += " (indirect)"
		}
		wanted := r.Wanted
		if wanted == "" {
			wanted = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, r.Current, wanted, r.Latest, r.Source)
	}
	tw.Flush()
	fmt.Fprintln(Stdout, "\n`gtr update` moves to Wanted; for Latest change the range in gtr.json (or `gtr add <source>`).")
	return nil
}
