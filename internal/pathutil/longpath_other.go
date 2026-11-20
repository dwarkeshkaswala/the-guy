//go:build !windows

package pathutil

func longPath(p string) string { return p }
