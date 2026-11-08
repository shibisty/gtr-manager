package commands

import (
	"context"
	"fmt"
	"strings"

	"gtr-manager/internal/install"
)

// Install without arguments installs everything from gtr.json (and
// gtr.lock); with arguments it adds packages (see Add).
func Install(args []string) error {
	offline, args := takeFlag(args, "--offline")
	dev, rest, err := flags(args, "-D", "--dev")
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return add(rest, dev, offline)
	}
	if dev {
		return fmt.Errorf("--dev needs a package to add")
	}
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	return in.Install(context.Background(), install.Options{})
}

// Add adds packages: `gtr add github:owner/repo[#range]`, `gtr add -D file:../pkg`.
func Add(args []string) error {
	offline, args := takeFlag(args, "--offline")
	dev, rest, err := flags(args, "-D", "--dev")
	if err != nil {
		return err
	}
	return add(rest, dev, offline)
}

func add(args []string, dev, offline bool) error {
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	return in.Add(context.Background(), args, dev)
}

// Uninstall removes packages from gtr.json and the project.
func Uninstall(args []string) error {
	offline, args := takeFlag(args, "--offline")
	if err := noFlags(args); err != nil {
		return err
	}
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	return in.Remove(context.Background(), args)
}

// Update re-resolves the named packages (all without arguments) to the
// newest versions their ranges allow.
func Update(args []string) error {
	offline, args := takeFlag(args, "--offline")
	if err := noFlags(args); err != nil {
		return err
	}
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	up := map[string]bool{}
	for _, a := range args {
		up[a] = true
	}
	if len(up) == 0 {
		up["*"] = true
	}
	return in.Install(context.Background(), install.Options{Update: up})
}

// CI installs exactly what gtr.lock records and fails if gtr.lock does not
// match gtr.json.
func CI(args []string) error {
	offline, args := takeFlag(args, "--offline")
	if len(args) > 0 {
		return fmt.Errorf("ci takes no arguments (except --offline)")
	}
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	return in.Install(context.Background(), install.Options{Frozen: true})
}

// Sync carries manual go.mod edits (or an existing Go project's go.mod) over
// to gtr.json and regenerates go.mod; `--force` discards them instead.
func Sync(args []string) error {
	offline, args := takeFlag(args, "--offline")
	force, rest, err := flags(args, "--force", "-f")
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(rest, " "))
	}
	in, err := installer()
	if err != nil {
		return err
	}
	in.Offline = in.Offline || offline
	return in.Sync(context.Background(), force)
}

// noFlags rejects anything that looks like a flag.
func noFlags(args []string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf("unknown flag %s", a)
		}
	}
	return nil
}

// takeFlag removes every occurrence of the flag names from args.
func takeFlag(args []string, names ...string) (bool, []string) {
	set := false
	var rest []string
	for _, a := range args {
		matched := false
		for _, n := range names {
			matched = matched || a == n
		}
		if matched {
			set = true
		} else {
			rest = append(rest, a)
		}
	}
	return set, rest
}

// flags extracts a boolean flag (any of names) from args.
func flags(args []string, names ...string) (bool, []string, error) {
	set := false
	var rest []string
	for _, a := range args {
		matched := false
		for _, n := range names {
			if a == n {
				set, matched = true, true
			}
		}
		if matched {
			continue
		}
		if strings.HasPrefix(a, "-") {
			return false, nil, fmt.Errorf("unknown flag %s", a)
		}
		rest = append(rest, a)
	}
	return set, rest, nil
}
