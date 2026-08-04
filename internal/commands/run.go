package commands

import (
    "fmt"
    "os"
    "os/exec"
    "runtime"

    "gtr-manager/internal/manifest"
)

func Run(args []string) error {

    m, err := manifest.Load("gtr.json")
    if err != nil {
        return err
    }

    name := "start"

    if len(args) > 0 {
        name = args[0]
    }

    command, ok := m.Scripts[name]
    if !ok || command == "" {
        return nil
    }

    var cmd *exec.Cmd

    if runtime.GOOS == "windows" {
        cmd = exec.Command("cmd", "/C", command)
    } else {
        cmd = exec.Command("sh", "-c", command)
    }

    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    cmd.Stdin = os.Stdin

    fmt.Printf("> %s\n\n", command)

    return cmd.Run()
}
