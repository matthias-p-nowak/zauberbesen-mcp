package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunDirectCommand verifies arguments, stdin, cwd, and startup logging.
func TestRunDirectCommand(t *testing.T) {
	root := t.TempDir()
	var logs bytes.Buffer
	result, err := runCommand(context.Background(), nil, log.New(&logs, "", 0), commandSpec{
		name:  "bash",
		args:  []string{"-c", `printf '%s|' "$1"; pwd; cat`, "direct", "argument"},
		cwd:   root,
		stdin: "input",
	})
	if err != nil {
		t.Fatalf("run direct command: %v", err)
	}
	wantPrefix := "argument|" + root + "\ninput"
	if result.Stdout != wantPrefix || result.Stderr != "" || result.ExitCode != 0 || result.TimedOut {
		t.Fatalf("direct result = %#v, want stdout %q", result, wantPrefix)
	}
	if !strings.Contains(logs.String(), "command_start") || !strings.Contains(logs.String(), "exit_code=0") {
		t.Fatalf("command lifecycle not logged: %s", logs.String())
	}
}

// TestRunShellCommand verifies separated streams and nonzero exit status.
func TestRunShellCommand(t *testing.T) {
	result, err := runCommand(context.Background(), nil, log.New(&bytes.Buffer{}, "", 0), commandSpec{
		name: "bash",
		args: []string{"-lc", `printf out; printf err >&2; exit 7`},
	})
	if err != nil {
		t.Fatalf("run shell command: %v", err)
	}
	// A login shell may append host-configured logout control sequences.
	if !strings.HasPrefix(result.Stdout, "out") || result.Stderr != "err" || result.ExitCode != 7 || result.TimedOut {
		t.Fatalf("shell result = %#v", result)
	}
}

// TestRunCommandTruncatesStreams verifies independent 64 KiB stream limits.
func TestRunCommandTruncatesStreams(t *testing.T) {
	result, err := runCommand(context.Background(), nil, log.New(&bytes.Buffer{}, "", 0), commandSpec{
		name: "bash",
		args: []string{"-lc", `head -c 70000 /dev/zero | tr '\0' x; head -c 70000 /dev/zero | tr '\0' y >&2`},
	})
	if err != nil {
		t.Fatalf("run truncating command: %v", err)
	}
	if len(result.Stdout) != maxToolTextBytes || len(result.Stderr) != maxToolTextBytes || !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("truncated result = stdout:%d/%v stderr:%d/%v", len(result.Stdout), result.StdoutTruncated, len(result.Stderr), result.StderrTruncated)
	}
}

// TestCappedCommandBufferBoundsInvalidUTF8 verifies JSON-safe conversion stays capped.
func TestCappedCommandBufferBoundsInvalidUTF8(t *testing.T) {
	var output cappedCommandBuffer
	_, _ = output.Write(bytes.Repeat([]byte{0xff, 'a'}, maxToolTextBytes/2))
	text := output.String()
	if len(text) > maxToolTextBytes || !output.truncated {
		t.Fatalf("converted output = %d bytes, truncated:%v", len(text), output.truncated)
	}
}

// TestRunCommandTimeout verifies timeout termination and result reporting.
func TestRunCommandTimeout(t *testing.T) {
	started := time.Now()
	result, err := runCommand(context.Background(), nil, log.New(&bytes.Buffer{}, "", 0), commandSpec{
		name: "bash", args: []string{"-lc", "sleep 10"}, timeoutSeconds: 1,
	})
	if err != nil {
		t.Fatalf("run timed command: %v", err)
	}
	if !result.TimedOut || result.ExitCode == 0 {
		t.Fatalf("timeout result = %#v", result)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

// TestCommandTimeout verifies default, override, and invalid durations.
func TestCommandTimeout(t *testing.T) {
	if got, err := commandTimeout(0); err != nil || got != defaultCommandTimeout {
		t.Fatalf("default timeout = %s, %v", got, err)
	}
	if got, err := commandTimeout(2); err != nil || got != 2*time.Second {
		t.Fatalf("override timeout = %s, %v", got, err)
	}
	if _, err := commandTimeout(-1); err == nil {
		t.Fatal("negative timeout did not fail")
	}
}

// TestRunCommandStartFailure verifies missing executables are tool errors.
func TestRunCommandStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-executable")
	_, err := runCommand(context.Background(), nil, log.New(os.Stdout, "", 0), commandSpec{name: missing})
	if err == nil {
		t.Fatal("missing executable did not fail")
	}
}
