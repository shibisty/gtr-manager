package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gtr-manager/internal/home"
	"gtr-manager/internal/install"
	"gtr-manager/internal/manifest"
)

// globalLinks is ~/.gtr/links.json: packages registered with a bare
// `gtr link`, by name.
func globalLinks() (string, error) {
	l, err := home.New()
	if err != nil {
		return "", err
	}
	return filepath.Join(l.Root, "links.json"), nil
}

// Link uses a local copy of a package instead of its source (npm link):
//
//	gtr link                  in a package: register it (~/.gtr/links.json)
//	gtr link orm              in a project: use the registered copy of orm
//	gtr link ../orm.go/core   in a project: use the package in that directory
//	gtr link --list           the project's links and the registered packages
//
// Links are personal: they live in .gtr/links.json (ignored by git) and do
// not change gtr.json or gtr.lock. gtr ci refuses to run with links.
func Link(args []string) error {
	list := false
	var targets []string
	for _, a := range args {
		switch {
		case a == "--list" || a == "-l":
			list = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s; usage: gtr link [name|path]... [--list]", a)
		default:
			targets = append(targets, a)
		}
	}
	gpath, err := globalLinks()
	if err != nil {
		return err
	}
	global, err := install.LoadLinks(gpath)
	if err != nil {
		return err
	}
	if list {
		return printLinks(global)
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return fmt.Errorf("no gtr.json in %s or above", wd)
	}
	if len(targets) == 0 {
		m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
		if err != nil {
			return err
		}
		if m.Name == "" {
			return errors.New("gtr.json has no name")
		}
		if prev, ok := global[m.Name]; ok && prev != dir {
			fmt.Fprintf(Stderr, "warning: %s was registered from %s\n", m.Name, prev)
		}
		global[m.Name] = dir
		if err := install.SaveLinks(gpath, global); err != nil {
			return err
		}
		fmt.Fprintf(Stdout, "Registered %s → %s; use it in a project with `gtr link %s`\n", m.Name, dir, m.Name)
		return nil
	}

	root, _, err := install.FindRoot(dir)
	if err != nil {
		return err
	}
	lpath := filepath.Join(root, filepath.FromSlash(install.LinksFile))
	links, err := install.LoadLinks(lpath)
	if err != nil {
		return err
	}
	for _, t := range targets {
		name, target, err := linkTarget(t, wd, global)
		if err != nil {
			return err
		}
		if target == root || target == dir {
			return fmt.Errorf("%s cannot link itself", name)
		}
		links[name] = target
	}
	if err := install.SaveLinks(lpath, links); err != nil {
		return err
	}
	if err := install.Gitignore(root, ".gtr/"); err != nil {
		return err
	}
	return reinstall(root)
}

// linkTarget resolves `gtr link <arg>`: a directory with a gtr.json, or the
// name of a registered package.
func linkTarget(arg, wd string, global map[string]string) (name, dir string, err error) {
	if strings.ContainsAny(arg, `/\`) || strings.HasPrefix(arg, ".") || filepath.IsAbs(arg) {
		dir = arg
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(wd, dir)
		}
		dir = filepath.Clean(dir)
		m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
		if err != nil {
			return "", "", fmt.Errorf("gtr link %s: %w", arg, err)
		}
		if m.Name == "" {
			return "", "", fmt.Errorf("%s: gtr.json has no name", dir)
		}
		return m.Name, dir, nil
	}
	dir, ok := global[arg]
	if !ok {
		return "", "", fmt.Errorf("%s is not registered; run `gtr link` in its directory, or give a path: gtr link ../%s", arg, arg)
	}
	if _, err := os.Stat(filepath.Join(dir, manifest.FileName)); err != nil {
		return "", "", fmt.Errorf("%s was registered from %s, which has no gtr.json any more; run `gtr link` there again", arg, dir)
	}
	return arg, dir, nil
}

// Unlink removes links of the project and reinstalls from gtr.lock:
//
//	gtr unlink orm        stop using the local copy of orm
//	gtr unlink            remove every link of the project
//	gtr unlink --global   in a package: unregister it
func Unlink(args []string) error {
	globalFlag := false
	var names []string
	for _, a := range args {
		switch {
		case a == "--global" || a == "-g":
			globalFlag = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s; usage: gtr unlink [name]... [--global]", a)
		default:
			names = append(names, a)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	dir := findManifestDir(wd)
	if dir == "" {
		return fmt.Errorf("no gtr.json in %s or above", wd)
	}
	if globalFlag {
		gpath, err := globalLinks()
		if err != nil {
			return err
		}
		global, err := install.LoadLinks(gpath)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			m, err := manifest.Load(filepath.Join(dir, manifest.FileName))
			if err != nil {
				return err
			}
			names = []string{m.Name}
		}
		for _, n := range names {
			if _, ok := global[n]; !ok {
				return fmt.Errorf("%s is not registered", n)
			}
			delete(global, n)
			fmt.Fprintf(Stdout, "Unregistered %s\n", n)
		}
		return install.SaveLinks(gpath, global)
	}
	root, _, err := install.FindRoot(dir)
	if err != nil {
		return err
	}
	lpath := filepath.Join(root, filepath.FromSlash(install.LinksFile))
	links, err := install.LoadLinks(lpath)
	if err != nil {
		return err
	}
	if len(links) == 0 {
		return errors.New("the project has no links")
	}
	if len(names) == 0 {
		links = map[string]string{}
	}
	for _, n := range names {
		if _, ok := links[n]; !ok {
			return fmt.Errorf("%s is not linked", n)
		}
		delete(links, n)
	}
	if err := install.SaveLinks(lpath, links); err != nil {
		return err
	}
	return reinstall(root)
}

func reinstall(root string) error {
	in, err := installer()
	if err != nil {
		return err
	}
	in.Dir = root
	return in.Install(context.Background(), install.Options{})
}

func printLinks(global map[string]string) error {
	if wd, err := os.Getwd(); err == nil {
		if dir := findManifestDir(wd); dir != "" {
			if root, _, err := install.FindRoot(dir); err == nil {
				links, err := install.LoadLinks(filepath.Join(root, filepath.FromSlash(install.LinksFile)))
				if err != nil {
					return err
				}
				fmt.Fprintln(Stdout, "Linked in this project:")
				printMap(links)
			}
		}
	}
	fmt.Fprintln(Stdout, "Registered (gtr link <name>):")
	printMap(global)
	return nil
}

func printMap(m map[string]string) {
	if len(m) == 0 {
		fmt.Fprintln(Stdout, "  (none)")
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(Stdout, "  %s → %s\n", k, m[k])
	}
}
