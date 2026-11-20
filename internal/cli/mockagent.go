package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// RunMockAgent implements the built-in deterministic mock coding agent
// (`guy mock-agent`), used by tests and `guy demo`. Behavior is driven by the
// GUY_MOCK_SPEC environment variable (JSON) — same schema as the Python
// implementation:
//
//	{"write": [{"path": "x.txt", "content": "hi"}],
//	 "stdout": "text", "sleep": 1.5, "exit": 0, "leak_secret": true,
//	 "spawn_child": {"seconds": 60}}
//
// It always writes the received prompt to mock-prompt-received.txt in cwd so
// tests can verify prompt transport (spaces, unicode, quotes).
func RunMockAgent(args []string) int {
	for _, a := range args {
		if a == "--help" {
			fmt.Println("the-guy mock agent: deterministic, no model calls")
			return 0
		}
	}
	// Deterministic file assertion used as required_tests evidence in the demo:
	// guy mock-agent --check-file <path> <expected-content>
	for i, a := range args {
		if a == "--check-file" && i+2 < len(args) {
			cwd, _ := os.Getwd()
			data, err := os.ReadFile(join(cwd, args[i+1]))
			if err != nil {
				fmt.Fprintf(os.Stderr, "[mock] check-file: %v\n", err)
				return 1
			}
			if string(data) != args[i+2] {
				fmt.Fprintf(os.Stderr, "[mock] check-file: %q != %q\n", string(data), args[i+2])
				return 1
			}
			fmt.Printf("[mock] check-file OK: %s\n", args[i+1])
			return 0
		}
	}
	prompt := ""
	for i, a := range args {
		if a == "--prompt" && i+1 < len(args) {
			prompt = strings.Join(args[i+1:], " ")
			break
		}
	}
	spec := map[string]any{}
	if raw := os.Getenv("GUY_MOCK_SPEC"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			fmt.Fprintln(os.Stderr, "[mock] invalid GUY_MOCK_SPEC; ignoring")
		}
	}
	cwd, _ := os.Getwd()
	_ = os.WriteFile(join(cwd, "mock-prompt-received.txt"), []byte(prompt), 0o644)

	if writes, ok := spec["write"].([]any); ok {
		for _, w := range writes {
			entry, ok := w.(map[string]any)
			if !ok {
				continue
			}
			path, _ := entry["path"].(string)
			content, _ := entry["content"].(string)
			if path == "" {
				continue
			}
			target := join(cwd, path)
			_ = os.MkdirAll(parentDir(target), 0o755)
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "[mock] failed to write %s: %v\n", path, err)
				continue
			}
			fmt.Printf("[mock] wrote %s\n", path)
		}
	}
	if stdout, ok := spec["stdout"].(string); ok && stdout != "" {
		fmt.Println(stdout)
	}
	if leak, ok := spec["leak_secret"].(bool); ok && leak {
		fmt.Println("api_key=AKIAIOSFODNN7EXAMPLE")
		fmt.Println("Authorization: Bearer abcdefghijklmnop.qrstuvwxyz")
		fmt.Println("connection string: postgres://admin:SuperSecret123@db.example.com/prod")
	}
	if child, ok := spec["spawn_child"].(map[string]any); ok {
		seconds := 60
		if s, ok := child["seconds"].(float64); ok {
			seconds = int(s)
		}
		self, err := os.Executable()
		if err == nil {
			childSpec, _ := json.Marshal(map[string]any{"sleep": float64(seconds)})
			cmd := exec.Command(self, "mock-agent")
			cmd.Env = append(os.Environ(), "GUY_MOCK_SPEC="+string(childSpec))
			if err := cmd.Start(); err == nil {
				_ = os.WriteFile(join(cwd, "mock-child.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
				fmt.Printf("[mock] spawned child pid %d\n", cmd.Process.Pid)
			}
		}
	}
	if sleep, ok := spec["sleep"].(float64); ok && sleep > 0 {
		time.Sleep(time.Duration(sleep * float64(time.Second)))
	}
	exit := 0
	if e, ok := spec["exit"].(float64); ok {
		exit = int(e)
	}
	return exit
}

func join(dir, name string) string {
	if len(name) > 0 && (name[0] == '/' || (len(name) > 2 && name[1] == ':')) {
		return name
	}
	return dir + string(os.PathSeparator) + name
}

func parentDir(p string) string {
	idx := strings.LastIndexAny(p, `/\`)
	if idx <= 0 {
		return "."
	}
	return p[:idx]
}
