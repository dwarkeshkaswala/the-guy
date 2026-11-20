// Package pathutil normalizes filesystem paths for comparison. On Windows,
// Go-constructed paths may carry 8.3 short names (TMP is often
// C:\Users\NAME~1\...) while git reports long names; Normalize expands short
// names and lowercases (Windows is case-insensitive) so path equality checks
// are reliable cross-source (git output vs Go filepath joins).
package pathutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// Normalize returns a canonical comparison form of p.
func Normalize(p string) string {
	p = longPath(filepath.Clean(p))
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.ToSlash(p)
}

// HasPrefix reports whether path p is inside directory prefix (or equal).
func HasPrefix(p, prefix string) bool {
	np, nprefix := Normalize(p), Normalize(prefix)
	return np == nprefix || strings.HasPrefix(np, nprefix+"/")
}
