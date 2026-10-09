package dirhash

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// referenceHash is an independent transcription of x/mod's Hash1.
func referenceHash(files map[string]string) string {
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256([]byte(files[n])), n)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}
