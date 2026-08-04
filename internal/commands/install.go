package commands

import (
    "fmt"
    "gtr-manager/internal/manifest"
    "gtr-manager/internal/repositories"
    "gtr-manager/internal/dependency"
)

type GithubRepository struct {
    DefaultBranch string `json:"default_branch"`
}

func Install(args []string) error {

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

    if m.Dependencies == nil {
        m.Dependencies = map[string]string{}
    }

    if version, ok := m.Dependencies[dep.Repository]; ok {
        return fmt.Errorf(
            "package %q is already installed (version %s)\nuse 'gtr uninstall %s && gtr install %s'",
            dep.Repository,
            version,
            dep.Repository,
            dep.Repository,
            dep.Repository,
        )
    }

    m.Dependencies[dep.Type + ":" + dep.Repository] = dep.Version

    err = manifest.Save("gtr.json", m)
    if err != nil {
        return err
    }

    switch dep.Type {

        case "github":
            return repositories.InstallGithub(dep)

        // case "gitlab":
        //     repositories.InstallGitlab(dep)

        // case "bitbucket":
        //     repositories.InstallBitbucket(dep)

        // case "git":
        //     repositories.InstallGit(dep)

        // case "gtr":
        //     repositories.InstallGTR(dep)
    }

    return nil
}
