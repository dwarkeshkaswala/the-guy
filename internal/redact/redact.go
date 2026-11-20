// Package redact provides secret redaction, path containment and safe
// identifier helpers. All untrusted text (agent output, repo content, plan
// files) passes through Redact before reaching logs or the terminal.
package redact

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(sk|rk)-[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret|token|password|passwd|pwd|authorization)\b['"]?\s*[:=]\s*\S+`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@`),
}

// Redact replaces common secret formats with [REDACTED].
func Redact(text string) string {
	for _, p := range patterns {
		text = p.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}

// EnsureWithin returns child resolved and reports whether it stays inside parent.
func EnsureWithin(parent, child string) (string, error) {
	absParent, err := filepath.Abs(parent)
	if err != nil {
		return "", err
	}
	absChild, err := filepath.Abs(child)
	if err != nil {
		return "", err
	}
	if absChild != absParent && !strings.HasPrefix(strings.ToLower(absChild)+string(filepath.Separator), strings.ToLower(absParent)+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes boundary %s", absChild, absParent)
	}
	return absChild, nil
}

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Slug turns arbitrary text into a safe filesystem-name slug.
func Slug(text string, maxLen int) string {
	s := strings.Trim(slugRe.ReplaceAllString(text, "-"), "-.")
	s = strings.ToLower(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "task"
	}
	return s
}

var refRe = regexp.MustCompile(`[^A-Za-z0-9._/-]+`)
var dashRe = regexp.MustCompile(`-{2,}`)

// SafeRef sanitizes a string for use as one component of a git ref name.
func SafeRef(name string) string {
	s := refRe.ReplaceAllString(strings.TrimSpace(name), "-")
	s = dashRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-./")
	if s == "" {
		s = "branch"
	}
	if strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	if s == "" {
		s = "branch"
	}
	if strings.HasSuffix(s, ".lock") {
		s = strings.TrimSuffix(s, ".lock")
	}
	if s == "" {
		s = "branch"
	}
	return s
}

var quoteNeeded = regexp.MustCompile(`[^\w@%+=:,./-]`)

// DisplayArgv renders an argv list as a redacted, copy-safe command string.
func DisplayArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || quoteNeeded.MatchString(a) {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			parts[i] = a
		}
	}
	return Redact(strings.Join(parts, " "))
}
