package redact

import (
	"strings"
	"testing"
)

func TestRedactCommonSecrets(t *testing.T) {
	text := "key sk-ABCDEF1234567890abcd token ghp_" + strings.Repeat("A", 30) + "\n" +
		"aws AKIAIOSFODNN7EXAMPLE\n" +
		"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig\n" +
		"api_key = hunter2secret\n" +
		"postgres://admin:SuperSecret123@db.example.com/x\n" +
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----\n"
	out := Redact(text)
	for _, secret := range []string{"sk-ABCDEF1234567890abcd", "AKIAIOSFODNN7EXAMPLE",
		"SuperSecret123", "hunter2secret", "PRIVATE KEY-----\nMIIE"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	if strings.Count(out, "[REDACTED]") < 5 {
		t.Fatalf("expected multiple redactions, got: %s", out)
	}
}

func TestRedactLeavesNormalText(t *testing.T) {
	plain := "git checkout -b guy/TASK-001-mock-task"
	if Redact(plain) != plain {
		t.Fatal("normal command text was modified")
	}
}

func TestSlugAndSafeRef(t *testing.T) {
	if s := Slug("Implement searchable catalog!!!", 20); s != "implement-searchable" {
		t.Fatalf("slug: %q", s)
	}
	if s := SafeRef("--force"); s != "force" {
		t.Fatalf("safe_ref: %q", s)
	}
	if s := SafeRef(""); s != "branch" {
		t.Fatalf("safe_ref empty: %q", s)
	}
}

func TestEnsureWithin(t *testing.T) {
	parent := t.TempDir()
	if _, err := EnsureWithin(parent, parent+string([]byte{'/', 'a'})); err != nil {
		t.Fatalf("contained path rejected: %v", err)
	}
	if _, err := EnsureWithin(parent, parent+"/../../escape"); err == nil {
		t.Fatal("escaping path accepted")
	}
}

func TestDisplayArgvRedacts(t *testing.T) {
	out := DisplayArgv([]string{"claude", "-p", "use api_key=ABCDEF123456 please"})
	if strings.Contains(out, "ABCDEF123456") {
		t.Fatal("secret visible in argv display")
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatal("redaction marker missing")
	}
}
