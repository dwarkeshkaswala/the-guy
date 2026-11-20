//go:build windows

package pathutil

import (
	"path/filepath"
	"strings"
	"syscall"
)

// longPath expands 8.3 short path components (NAME~1) to long names.
// GetLongPathName requires the path to exist, so for not-yet-created paths
// the deepest existing ancestor is expanded and the remainder reattached.
// Returns p unchanged when no expansion applies.
func longPath(p string) string {
	if p == "" || !strings.Contains(p, "~") {
		return p
	}
	if out, ok := tryLongPath(p); ok {
		return out
	}
	full := p
	var tail []string
	for {
		parent := filepath.Dir(full)
		if parent == full {
			break
		}
		tail = append([]string{filepath.Base(full)}, tail...)
		full = parent
		if out, ok := tryLongPath(full); ok {
			return filepath.Join(append([]string{out}, tail...)...)
		}
	}
	return p
}

func tryLongPath(p string) (string, bool) {
	utf16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p, false
	}
	buf := make([]uint16, len(p)+260)
	n, err := syscall.GetLongPathName(utf16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) >= len(buf) {
		return p, false
	}
	return syscall.UTF16ToString(buf[:n]), true
}
