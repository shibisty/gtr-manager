package commands

import (
    "bufio"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "gtr-manager/internal/manifest"
)

func Init(args []string) error {
    if _, err := os.Stat("gtr.json"); err == nil {
        return fmt.Errorf("this directory is already initialized as a GTR project")
    }

    wd, err := os.Getwd()
    if err != nil {
        return err
    }

    defaultName := filepath.Base(wd)
    if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
        defaultName = strings.TrimSpace(args[0])
    }

    reader := bufio.NewReader(os.Stdin)

    m := &manifest.Manifest{
        Name:         ask(reader, "What is the package name?", defaultName),
        Version:      ask(reader, "What is the package version?", "1.0.0"),
        Author:       ask(reader, "What is your name?", ""),
        Website:      ask(reader, "What is your website?", ""),
        Entrypoint:   ask(reader, "What is the entrypoint?", "main.go"),
        Engine:       ">=1.26",
        Dependencies: map[string]string{},
        Scripts: map[string]string{
            "build": "go build",
            "test":  "go test",
        },
    }

    return manifest.Save("gtr.json", m)
}

func ask(reader *bufio.Reader, question, def string) string {

    if def != "" {
        fmt.Printf("%s (%s): ", question, def)
    } else {
        fmt.Printf("%s: ", question)
    }

    text, _ := reader.ReadString('\n')
    text = strings.TrimSpace(text)

    if text == "" {
        return def
    }

    return text
}
