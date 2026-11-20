//go:build windows

package pathutil

import (
	"path/filepath"
	"syscall"
	"testing"
)

func shortPath(t *testing.T, p string) string {
	t.Helper()
	utf16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, len(p)+260)
	n, err := syscall.GetShortPathName(utf16, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) >= len(buf) {
		return p
	}
	return syscall.UTF16ToString(buf[:n])
}

func TestLongPathShortName(t *testing.T) {
	base := t.TempDir()
	short := shortPath(t, base)
	if short == base {
		t.Skip("filesystem reports no short names for the temp dir")
	}
	if !filepath.IsAbs(short) {
		t.Skipf("unexpected short form: %q", short)
	}
	if Normalize(short) != Normalize(base) {
		t.Fatalf("short/long mismatch: %q vs %q", Normalize(short), Normalize(base))
	}
	// non-existent child of a short-named dir must still compare equal
	if !HasPrefix(filepath.Join(short, "child", "file.txt"), base) {
		t.Fatal("short-form child not detected inside long-form base")
	}
}
