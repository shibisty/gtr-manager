package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"gtr-manager/internal/registry"
)

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// parseRegistryFlag takes --registry URL (or --registry=URL) out of args.
func parseRegistryFlag(args []string) (url string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--registry":
			if i+1 >= len(args) {
				return "", nil, errors.New("--registry needs a URL")
			}
			i++
			url = args[i]
		case strings.HasPrefix(a, "--registry="):
			url = strings.TrimPrefix(a, "--registry=")
		default:
			rest = append(rest, a)
		}
	}
	return registry.Normalize(url), rest, nil
}

func clientFor(url string) *registry.Client {
	if url == "" {
		return registry.New()
	}
	return &registry.Client{URL: url, Token: registry.Token(url)}
}

// Login saves an API token of a registry and makes it the registry in use:
//
//	gtr login                         email and password, for the registry in use
//	gtr login --registry http://localhost:4873
//	gtr login --token gtr_...         a token made on the registry's dashboard (CI)
func Login(args []string) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	token, email := "", ""
	for i := 0; i < len(rest); i++ {
		switch a := rest[i]; {
		case a == "--token" && i+1 < len(rest):
			i++
			token = rest[i]
		case strings.HasPrefix(a, "--token="):
			token = strings.TrimPrefix(a, "--token=")
		case a == "--email" && i+1 < len(rest):
			i++
			email = rest[i]
		default:
			return fmt.Errorf("unknown argument %q; usage: gtr login [--registry <url>] [--email <email>] [--token <token>]", a)
		}
	}
	if url == "" {
		url = registry.URL()
	}
	ctx, stop := signalContext()
	defer stop()
	c := &registry.Client{URL: url}
	name := ""
	if token == "" {
		in := bufio.NewReader(Stdin)
		if email == "" {
			fmt.Fprintf(Stdout, "Email: ")
			line, _ := in.ReadString('\n')
			email = strings.TrimSpace(line)
		}
		fmt.Fprintf(Stdout, "Password: ")
		password, err := readPassword(in)
		fmt.Fprintln(Stdout)
		if err != nil {
			return err
		}
		host, _ := os.Hostname()
		if token, name, err = c.Login(ctx, email, password, "gtr on "+host); err != nil {
			return err
		}
	} else {
		c.Token = token
		if name, email, err = c.Me(ctx); err != nil {
			return err
		}
	}
	cfg, err := registry.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]string{}
	}
	cfg.Registry, cfg.Tokens[url] = url, token
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "Logged in to %s as %s <%s>\n", url, name, email)
	return nil
}

// Logout revokes the token of the registry in use and forgets it.
func Logout(args []string) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New("usage: gtr logout [--registry <url>]")
	}
	c := clientFor(url)
	ctx, stop := signalContext()
	defer stop()
	if c.Token != "" {
		if err := c.Logout(ctx); err != nil {
			fmt.Fprintln(Stderr, "warning:", err)
		}
	}
	cfg, err := registry.LoadConfig()
	if err != nil {
		return err
	}
	delete(cfg.Tokens, c.URL)
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "Logged out of %s\n", c.URL)
	return nil
}

// Whoami prints the user of the registry token.
func Whoami(args []string) error {
	url, _, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	c := clientFor(url)
	if c.Token == "" {
		return fmt.Errorf("not logged in to %s: run `gtr login`", c.URL)
	}
	ctx, stop := signalContext()
	defer stop()
	name, email, err := c.Me(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "%s <%s> on %s\n", name, email, c.URL)
	return nil
}

// Search lists registry packages matching a query.
func Search(args []string) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	res, err := clientFor(url).Search(ctx, strings.Join(rest, " "), 30)
	if err != nil {
		return err
	}
	if len(res) == 0 {
		fmt.Fprintln(Stdout, "No packages found")
		return nil
	}
	tw := tabwriter.NewWriter(Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLATEST\tDESCRIPTION")
	for _, p := range res {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, p.Latest, p.Description)
	}
	return tw.Flush()
}

// Info shows a registry package: description, owners and versions.
func Info(args []string) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: gtr info <name> [--registry <url>]")
	}
	ctx, stop := signalContext()
	defer stop()
	c := clientFor(url)
	ix, err := c.Package(ctx, rest[0])
	if registry.IsNotFound(err) {
		return fmt.Errorf("there is no package %s in %s", rest[0], c.URL)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "%s %s\n", ix.Name, ix.Latest)
	if ix.Description != "" {
		fmt.Fprintln(Stdout, ix.Description)
	}
	fmt.Fprintln(Stdout)
	for _, kv := range [][2]string{{"repository", ix.Repository}, {"homepage", ix.Homepage}, {"license", ix.License},
		{"owners", strings.Join(ix.Owners, ", ")}, {"downloads", fmt.Sprint(ix.Downloads)}} {
		if kv[1] != "" {
			fmt.Fprintf(Stdout, "%-11s %s\n", kv[0]+":", kv[1])
		}
	}
	fmt.Fprintf(Stdout, "\ninstall:    gtr add %s\n\nversions:\n", ix.Name)
	tw := tabwriter.NewWriter(Stdout, 0, 0, 2, ' ', 0)
	for _, v := range ix.Versions {
		mark := ""
		if v.Yanked {
			mark = "yanked"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", v.Version, v.PublishedAt.Local().Format(time.DateOnly), mark)
	}
	return tw.Flush()
}

// Yank hides a published version from new installs (--undo: brings it back).
// Projects whose gtr.lock has it keep installing it.
func Yank(args []string, undo bool) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	var target string
	for _, a := range rest {
		switch {
		case a == "--undo":
			undo = true
		case target == "":
			target = a
		default:
			return errors.New("usage: gtr yank <name>@<version> [--undo]")
		}
	}
	name, version, ok := strings.Cut(target, "@")
	if !ok || name == "" || version == "" {
		return errors.New("usage: gtr yank <name>@<version> [--undo]")
	}
	ctx, stop := signalContext()
	defer stop()
	if err := clientFor(url).Yank(ctx, name, version, !undo); err != nil {
		return err
	}
	if undo {
		fmt.Fprintf(Stdout, "%s@%s is installable again\n", name, version)
	} else {
		fmt.Fprintf(Stdout, "Yanked %s@%s: new installs skip it; locked projects keep it\n", name, version)
	}
	return nil
}

// Owner lists and changes who may publish a package:
//
//	gtr owner <name>
//	gtr owner add <name> <email>
//	gtr owner remove <name> <email>
func Owner(args []string) error {
	url, rest, err := parseRegistryFlag(args)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	c := clientFor(url)
	var owners []string
	switch {
	case len(rest) == 1 || len(rest) == 2 && (rest[0] == "list" || rest[0] == "ls"):
		owners, err = c.Owners(ctx, rest[len(rest)-1])
	case len(rest) == 3 && rest[0] == "add":
		owners, err = c.AddOwner(ctx, rest[1], rest[2])
	case len(rest) == 3 && (rest[0] == "remove" || rest[0] == "rm"):
		owners, err = c.RemoveOwner(ctx, rest[1], rest[2])
	default:
		return errors.New("usage: gtr owner <name> | gtr owner add <name> <email> | gtr owner remove <name> <email>")
	}
	if err != nil {
		return err
	}
	sort.Strings(owners)
	for _, o := range owners {
		fmt.Fprintln(Stdout, o)
	}
	return nil
}
