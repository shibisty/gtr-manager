# GTR Manager

GTR Manager is the package manager component of GTR.

It is responsible for:

- Project initialization
- Dependency management
- Script execution

GTR Manager is executed by the `gtr` launcher. End users normally interact only with the `gtr` command.

---

# Installation

GTR Manager is distributed together with GTR.

Check installed version:

```bash
gtr version
```

---

# Project Manifest

Every project contains a `gtr.json` file.

Example:

```json
{
    "name": "hello-world",
    "version": "1.0.0",
    "author": "John Doe",
    "website": "https://example.com",
    "entrypoint": "main.go",

    "dependencies": {
        "repo/router": "*",
        "github:golang/example": "^1"
    },

    "scripts": {
        "start": "go run .",
        "build": "go build",
        "test": "go test ./..."
    }
}
```

---

# Commands

## Create a project

Create a new project.

```bash
gtr new project-name
```

If the directory does not exist, it will be created.

If the directory exists, it must be empty.

---

## Initialize a project

Initialize the current directory.

```bash
gtr init
```

Specify project name.

```bash
gtr init my-project
```

During initialization several questions will be asked:

```
What name of package?
What version of package?
What is your name?
What is your website?
What is entrypoint?
```

---

## Install a package

Install a dependency.

```bash
gtr install repo/router
```

Default version:

```json
"repo/router": "*"
```

Specify version:

```bash
gtr install repo/router@^1
```

```bash
gtr install repo/router@1.2
```

```bash
gtr install repo/router@1.2.5
```

---

## Uninstall a package

Remove a dependency.

```bash
gtr uninstall repo/router
```

---

## Run scripts

Run the `start` script.

```bash
gtr run
```

or

```bash
gtr start
```

Run a named script.

```bash
gtr run build
```

Example:

```json
{
    "scripts": {
        "start": "go run .",
        "build": "go build",
        "test": "go test ./..."
    }
}
```

---

# Package Sources

## GitHub

```
github:user/package
```

---

# Version Syntax

Latest version

```
*
```

Latest compatible major version

```
^1
```

Exact version

```
1.2.5
```

Major version

```
1
```

Equivalent to

```
1.0.0
```

Minor version

```
1.2
```

Equivalent to

```
1.2.0
```

Pre-release

```
1.2.5-rc
```

---

# Exit Codes

| Code | Description |
|------:|-------------|
| 0 | Success |
| 1 | Error |

---

# License

MIT License.
