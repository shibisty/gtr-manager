package repositories

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "strings"

    "gtr-manager/internal/archive"
    "gtr-manager/internal/dependency"
)

type GithubRepository struct {
    DefaultBranch string `json:"default_branch"`
}

func InstallGithub(dep *dependency.Dependency) error {

    repo := strings.TrimPrefix(dep.Repository, "github:")

    info, err := getRepository(repo)
    if err != nil {
        return err
    }

    url := fmt.Sprintf(
        "https://github.com/%s/archive/refs/heads/%s.zip",
        repo,
        info.DefaultBranch,
    )
    fmt.Printf(url)

    exe, err := os.Executable()
    if err != nil {
        return err
    }

    root := filepath.Dir(exe)

    cache := filepath.Join(root, "cache", "github", strings.ReplaceAll(repo, "/", "_")+".zip")

    dst := filepath.Join("packages", "github", repo)

    if err := os.MkdirAll(filepath.Dir(cache), 0755); err != nil {
        return err
    }

    if err := download(url, cache); err != nil {
        return err
    }

    if err := os.RemoveAll(dst); err != nil {
        return err
    }

    if err := archive.Extract(cache, dst); err != nil {
        return err
    }

    return flatten(dst)
}

func getRepository(repo string) (*GithubRepository, error) {

    url := "https://api.github.com/repos/" + repo

    resp, err := http.Get(url)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("github repository not found")
    }

    var r GithubRepository

    if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
        return nil, err
    }

    return &r, nil
}

func download(url, filename string) error {

    resp, err := http.Get(url)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    out, err := os.Create(filename)
    if err != nil {
        return err
    }
    defer out.Close()

    _, err = io.Copy(out, resp.Body)

    return err
}

// GitHub распаковывает archive в папку вида project-main.
// Эта функция убирает лишний уровень.
func flatten(dst string) error {

    entries, err := os.ReadDir(dst)
    if err != nil {
        return err
    }

    if len(entries) != 1 || !entries[0].IsDir() {
        return nil
    }

    tmp := filepath.Join(dst, entries[0].Name())

    items, err := os.ReadDir(tmp)
    if err != nil {
        return err
    }

    for _, item := range items {

        err := os.Rename(
            filepath.Join(tmp, item.Name()),
            filepath.Join(dst, item.Name()),
        )

        if err != nil {
            return err
        }
    }

    return os.Remove(tmp)
}
