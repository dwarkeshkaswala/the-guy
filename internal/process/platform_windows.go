//go:build windows

package process

import (
	"fmt"
	"os/exec"
	"strings"
)

// KillTree terminates an entire process tree via taskkill /F /T.
// (Job Objects are a possible future enhancement; taskkill /T has proven
// reliable in the Python suite and its Go port.)
func KillTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprint(pid)).Run()
}

// PidAlive checks for the pid in tasklist output.
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+fmt.Sprint(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "\""+fmt.Sprint(pid)+"\"")
}

func setSysProcAttr(*exec.Cmd) {}
