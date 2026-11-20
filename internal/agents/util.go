package agents

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// runCapture runs a read-only probe command; returns (output, ok).
func runCapture(argv []string, timeout time.Duration) (string, bool) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Start(); err != nil {
		return "", false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", false
		}
		return combined.String(), true
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return "", false
	}
}

func contains(s, substr string) bool { return strings.Contains(s, substr) }

func joinList(parts []string) string { return strings.Join(parts, ", ") }

func firstLine(s string) string {
	line := strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

func marshalJSON(v any) ([]byte, error) { return json.Marshal(v) }
