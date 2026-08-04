package dependency

import (
    "fmt"
    "strings"
)

func ParseDependency(s string) (*Dependency, error) {

    version := "*"

    if i := strings.LastIndex(s, "@"); i != -1 {
        version = strings.TrimSpace(s[i+1:])
        s = strings.TrimSpace(s[:i])
    }

    if s == "" {
        return nil, fmt.Errorf("package name is required")
    }

    dep := &Dependency{
        Type:       "gtr",
        Repository: s,
        Version:    version,
    }

    switch {

    case strings.HasPrefix(s, "github:"):
        dep.Type = "github"
        dep.Repository = strings.TrimPrefix(s, "github:")

    case strings.HasPrefix(s, "gitlab:"):
        dep.Type = "gitlab"
        dep.Repository = strings.TrimPrefix(s, "gitlab:")

    case strings.HasPrefix(s, "bitbucket:"):
        dep.Type = "bitbucket"
        dep.Repository = strings.TrimPrefix(s, "bitbucket:")

    case strings.HasPrefix(s, "http://"),
        strings.HasPrefix(s, "https://"):

        dep.Type = "git"

    default:
        dep.Type = "gtr"
    }

    return dep, nil
}
