// Package resources embeds the views into the binary.
package resources

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Views are the templates of resources/views.
//
//go:embed views
var embedded embed.FS

// Views is the embedded resources directory.
var Views fs.FS = embedded

// dir is this package's directory in the source tree.
func dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// OnDisk reports whether the source of the views is here (go run in the
// project), so that development can read them from disk and see edits
// without restarting.
func OnDisk() bool {
	_, err := os.Stat(filepath.Join(dir(), "views"))
	return err == nil
}

// Disk is the resources directory on disk.
func Disk() fs.FS { return os.DirFS(dir()) }
