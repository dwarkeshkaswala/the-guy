// Package process runs agent subprocesses: argv arrays only (no shell), the
// worktree as cwd, streaming output into redacted size-capped logs, timeouts,
// cross-process cancellation (via a status poll callback), and process-tree
// termination (taskkill /F /T on Windows, process groups on POSIX).
package process

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/dwkaswala/the-guy/internal/redact"
)

// Outcome summarizes one execution.
type Outcome struct {
	ExitCode     int
	ExitCodeSet  bool
	TimedOut     bool
	Cancelled    bool
	SpawnError   string
	DurationMS   int64
	FailureClass string
	LogPath      string
}

// StatusPoll is consulted every ~300ms; "cancel-requested" kills the tree
// (used by `guy cancel` across processes).
type StatusPoll func() string
type OnStart func(pid int)

type cappedLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
}

const truncationNote = "\n[... log truncated to keep the most recent output ...]\n"

func newCappedLog(path string, maxBytes int64) *cappedLog {
	return &cappedLog{path: path, maxBytes: maxBytes}
}

func (l *cappedLog) open() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.file = f
	return nil
}

func (l *cappedLog) write(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return
	}
	_, _ = l.file.WriteString(redact.Redact(text))
	_ = l.file.Sync()
	if st, err := os.Stat(l.path); err == nil && st.Size() > l.maxBytes {
		l.shrink(st.Size())
	}
}

func (l *cappedLog) shrink(size int64) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	keep := l.maxBytes / 2
	var tail []byte
	if int64(len(data)) > keep {
		tail = data[int64(len(data))-keep:]
	} else {
		tail = data
	}
	_ = l.file.Close()
	_ = os.WriteFile(l.path, append([]byte(truncationNote), tail...), 0o644)
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		l.file = f
	}
}

func (l *cappedLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

// KillTree and PidAlive are implemented per-OS in platform_*.go.

// lineWriter buffers chunked writes into whole lines so redaction patterns
// cannot be split across chunks. It is safe for concurrent use.
type lineWriter struct {
	mu  sync.Mutex
	buf []byte
	log *cappedLog
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		idx := indexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		w.log.write(string(w.buf[:idx+1]))
		w.buf = w.buf[idx+1:]
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.log.write(string(w.buf) + "\n")
		w.buf = nil
	}
}

// Execute runs a subprocess to completion with timeout/cancel support.
// Output is consumed by assigning cmd.Stdout/Stderr to a writer (the exec
// package's copy goroutines are joined by Wait), never by StdoutPipe —
// Wait closes a StdoutPipe's read end and can race an in-flight drain,
// silently losing output.
func Execute(argv []string, cwd string, env []string, timeoutSec int,
	logPath string, maxLogBytes int64, statusPoll StatusPoll, onStart OnStart) Outcome {

	started := time.Now()
	log := newCappedLog(logPath, maxLogBytes)
	_ = log.open()
	header := fmt.Sprintf("=== the-guy run ===\ntime: %s\ncwd: %s\ncommand: %s\ntimeout: %ds\n\n",
		started.Format(time.RFC3339), cwd, redact.DisplayArgv(argv), timeoutSec)
	log.write(header)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	setSysProcAttr(cmd)
	writer := &lineWriter{log: log}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		log.write(fmt.Sprintf("\n[the-guy] failed to start process: %v\n", err))
		log.close()
		return Outcome{SpawnError: err.Error(), FailureClass: "spawn_error",
			DurationMS: time.Since(started).Milliseconds(), LogPath: logPath}
	}
	if onStart != nil {
		onStart(cmd.Process.Pid)
	}

	deadline := time.Duration(0)
	if timeoutSec > 0 {
		deadline = time.Duration(timeoutSec) * time.Second
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	var timeoutCh <-chan time.Time
	if deadline > 0 {
		timeoutCh = time.After(deadline)
	}
	poll := time.NewTicker(300 * time.Millisecond)
	defer poll.Stop()
	timedOut, cancelled := false, false
	var waitErr error
loop:
	for {
		select {
		case waitErr = <-waitCh:
			break loop
		case <-timeoutCh:
			KillTree(cmd.Process.Pid)
			timedOut = true
			select {
			case waitErr = <-waitCh:
			case <-time.After(30 * time.Second):
			}
			break loop
		case <-poll.C:
			if statusPoll != nil && statusPoll() == "cancel-requested" {
				KillTree(cmd.Process.Pid)
				cancelled = true
				select {
				case waitErr = <-waitCh:
				case <-time.After(30 * time.Second):
				}
				break loop
			}
		}
	}
	writer.flush()

	// `guy cancel` marks the run cancel-requested and kills the tree itself,
	// so the process can exit before the next poll tick observes the request.
	// Re-check once so a user cancel is never misclassified as a crash.
	if !cancelled && !timedOut && statusPoll != nil && statusPoll() == "cancel-requested" {
		cancelled = true
	}

	duration := time.Since(started).Milliseconds()
	exitCode, exitSet := exitCodeOf(waitErr)
	failureClass, note := "", ""
	switch {
	case cancelled:
		failureClass, note = "cancelled", "cancelled by user; process tree killed"
	case timedOut:
		failureClass, note = "timeout", fmt.Sprintf("timed out after %ds; process tree killed", timeoutSec)
	case !exitSet || exitCode != 0:
		failureClass = "nonzero"
		if !exitSet {
			note = "agent process died without an exit code"
		} else {
			note = fmt.Sprintf("agent exited with code %d", exitCode)
		}
	default:
		note = "agent exited 0 (note: exit 0 is not proof the work is correct)"
	}
	log.write(fmt.Sprintf("\n[the-guy] %s\n[the-guy] duration: %d ms\n", note, duration))
	log.close()
	return Outcome{
		ExitCode: exitCode, ExitCodeSet: exitSet, TimedOut: timedOut,
		Cancelled: cancelled, DurationMS: duration, FailureClass: failureClass,
		LogPath: logPath,
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func exitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return -1, false
}
