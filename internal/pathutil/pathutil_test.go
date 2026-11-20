package pathutil

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestNormalizeSlashAndCase(t *testing.T) {
	if runtime.GOOS == "windows" {
		a := `C:\Users\Some One\repo`
		b := "c:/users/some one/repo/"
		if Normalize(a) != Normalize(b) {
			t.Fatalf("normalize mismatch: %q vs %q", Normalize(a), Normalize(b))
		}
	} else {
		a := "/tmp/some dir/repo"
		b := "/tmp/some dir/repo/"
		if Normalize(a) != Normalize(b) {
			t.Fatalf("normalize mismatch: %q vs %q", Normalize(a), Normalize(b))
		}
	}
}

func TestHasPrefix(t *testing.T) {
	base := t.TempDir()
	inner := filepath.Join(base, "sub dir", "file.txt") // does NOT exist yet
	if !HasPrefix(inner, base) {
		t.Fatalf("inner path not detected: %q vs %q", Normalize(inner), Normalize(base))
	}
	if err := osMkdirAll(filepath.Dir(inner)); err != nil {
		t.Fatal(err)
	}
	if !HasPrefix(inner, base) {
		t.Fatal("inner path not detected after parent exists")
	}
	if HasPrefix(filepath.Join(base+"-other", "x"), base) {
		t.Fatal("sibling directory must not be detected as inside")
	}
}
