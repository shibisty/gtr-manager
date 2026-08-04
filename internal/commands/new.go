package commands

import (
    "fmt"
    "os"
    "path/filepath"
)

func New(args []string) error {
    if len(args) == 0 {
        return fmt.Errorf("project name is required")
    }

    dir := args[0]

    if st, err := os.Stat(dir); err == nil {

        if !st.IsDir() {
            return fmt.Errorf("%q is not a directory", dir)
        }

        entries, err := os.ReadDir(dir)
        if err != nil {
            return err
        }

        if len(entries) != 0 {
            return fmt.Errorf("directory %q is not empty", dir)
        }

    } else if os.IsNotExist(err) {

        if err := os.MkdirAll(dir, 0755); err != nil {
            return err
        }

    } else {
        return err
    }

    cwd, err := os.Getwd()
    if err != nil {
        return err
    }

    defer os.Chdir(cwd)

    if err := os.Chdir(dir); err != nil {
        return err
    }

    return Init([]string{filepath.Base(dir)})
}
