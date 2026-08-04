package commands

import (
    "fmt"
    "os"
    "path/filepath"

    "gtr-manager/internal/dependency"
    "gtr-manager/internal/manifest"
)

func Uninstall(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("package name is required")
    }

    m, err := manifest.Load("gtr.json")
    if err != nil {
        return err
    }

    dep, err := dependency.ParseDependency(args[0])
    if err != nil {
        return err
    }

    version, ok := m.Dependencies[dep.Repository]
    if !ok {
        return fmt.Errorf("package %q is not installed", dep.Repository)
    }

    delete(m.Dependencies, dep.Repository)

    if err := manifest.Save("gtr.json", m); err != nil {
        return err
    }

    var dir string

    switch dep.Type {
        case "github":
            dir = filepath.Join("packages", "github", dep.Repository)
            break
        // case "gitlab":
        //     dir = filepath.Join("packages", "gitlab", dep.Repository)
        // case "bitbucket":
        //     dir = filepath.Join("packages", "bitbucket", dep.Repository)
        // case "git":
        default: // gtr
            dir = filepath.Join("packages", dep.Repository)
            break
    }

    if dir != "" {
        _ = os.RemoveAll(dir)
    }

    fmt.Printf("Removed %s (%s)\n", dep.Repository, version)

    return nil
}
