// Package dirhash computes the "h1:" hash of a directory tree, the same
// algorithm Go uses in go.sum (golang.org/x/mod/sumdb/dirhash.Hash1):
//
//	h1:base64(sha256(for each file, sorted by name: hex(sha256(file)) + "  " + name + "\n"))
//
// Names are "<prefix>/<slash-separated relative path>".
package dirhash

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HashDir hashes every regular file under dir. skip, if not nil, excludes
// files by their slash-separated path relative to dir (e.g. files gtr
// generated itself).
func HashDir(dir, prefix string, skip func(rel string) bool) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("dirhash: %s is not a regular file", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip == nil || !skip(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		if strings.Contains(rel, "\n") {
			return "", errors.New("dirhash: file name contains a newline")
		}
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fh := sha256.New()
		_, err = io.Copy(fh, f)
		f.Close()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%x  %s\n", fh.Sum(nil), prefix+"/"+rel)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

// Short returns 8 hex characters of an h1 hash, for directory names.
func Short(h1 string) string {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h1, "h1:"))
	if err != nil || len(raw) < 4 {
		return "00000000"
	}
	return fmt.Sprintf("%x", raw[:4])
}
