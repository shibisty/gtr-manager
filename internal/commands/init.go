package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gtr-manager/internal/manifest"
)

// Init creates gtr.json in the current directory. The -y / --yes flag accepts
// the defaults without prompting (for scripts and CI).
func Init(args []string) error {
	yes := false
	var rest []string
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("unknown flag %s", a)
			}
			rest = append(rest, a)
		}
	}
	if len(rest) > 1 {
		return fmt.Errorf("too many arguments: %s", strings.Join(rest, " "))
	}
	if _, err := os.Stat(manifest.FileName); err == nil {
		return fmt.Errorf("this directory is already initialized as a GTR project")
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	defaultName := filepath.Base(wd)
	if len(rest) == 1 && strings.TrimSpace(rest[0]) != "" {
		defaultName = strings.TrimSpace(rest[0])
	}

	q := &asker{r: bufio.NewReader(Stdin), yes: yes}
	m := &manifest.Manifest{
		Name:         q.ask("What is the package name?", defaultName),
		Version:      q.ask("What is the package version?", "1.0.0"),
		Author:       q.ask("What is your name?", ""),
		Website:      q.ask("What is your website?", ""),
		Entrypoint:   q.ask("What is the entrypoint?", "main.go"),
		Engine:       ">=1.26",
		Dependencies: map[string]string{},
		Scripts: map[string]string{
			"start": "go run .",
			"build": "go build ./...",
			"test":  "go test ./...",
		},
	}
	if err := manifest.Save(manifest.FileName, m); err != nil {
		return err
	}
	fmt.Fprintf(Stdout, "Created %s\n", filepath.Join(wd, manifest.FileName))
	return nil
}

type asker struct {
	r   *bufio.Reader
	yes bool
	eof bool
}

func (q *asker) ask(question, def string) string {
	if q.yes || q.eof {
		return def
	}
	if def != "" {
		fmt.Fprintf(Stdout, "%s (%s): ", question, def)
	} else {
		fmt.Fprintf(Stdout, "%s: ", question)
	}
	text, err := q.r.ReadString('\n')
	if err != nil { // stdin is closed: use defaults from here on
		q.eof = true
		fmt.Fprintln(Stdout)
	}
	if text = strings.TrimSpace(text); text != "" {
		return text
	}
	return def
}
