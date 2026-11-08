package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

// TestInteractiveShellSession verifies start, stdin, poll, and separated output.
func TestInteractiveShellSession(t *testing.T) {
	manager := newProcessManager(log.New(&bytes.Buffer{}, "", 0), time.Minute)
	t.Cleanup(manager.Close)
	started, err := manager.Start(startProcessInput{
		Mode: "shell", Command: `read line; printf 'got:%s' "$line"; printf err >&2`,
	})
	if err != nil {
		t.Fatalf("start shell session: %v", err)
	}
	if started.Status != "running" || started.SessionID == "" {
		t.Fatalf("start result = %#v", started)
	}
	written, err := manager.WriteStdin(writeStdinInput{SessionID: started.SessionID, Input: "hello\n", Close: true})
	if err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	if written.Written != len("hello\n") {
		t.Fatalf("written bytes = %d", written.Written)
	}
	result := waitForProcessExit(t, manager, started.SessionID)
	if !strings.Contains(result.Stdout, "got:hello") || result.Stderr != "err" || result.ExitCode != 0 {
		t.Fatalf("shell session result = %#v", result)
	}

	drained, err := manager.Poll(started.SessionID)
	if err != nil {
		t.Fatalf("poll drained session: %v", err)
	}
	if drained.Stdout != "" || drained.Stderr != "" {
		t.Fatalf("poll did not drain output: %#v", drained)
	}
}

// TestInteractiveExecSession verifies direct mode and stdin closure.
func TestInteractiveExecSession(t *testing.T) {
	manager := newProcessManager(log.New(&bytes.Buffer{}, "", 0), time.Minute)
	t.Cleanup(manager.Close)
	started, err := manager.Start(startProcessInput{Mode: "exec", Executable: "cat"})
	if err != nil {
		t.Fatalf("start exec session: %v", err)
	}
	if _, err = manager.WriteStdin(writeStdinInput{SessionID: started.SessionID, Input: "payload", Close: true}); err != nil {
		t.Fatalf("write exec stdin: %v", err)
	}
	result := waitForProcessExit(t, manager, started.SessionID)
	if result.Stdout != "payload" || result.ExitCode != 0 {
		t.Fatalf("exec session result = %#v", result)
	}
}

// TestOutputRing verifies newest-data retention, dropped counts, and draining.
func TestOutputRing(t *testing.T) {
	var ring outputRing
	input := strings.Repeat("x", maxToolTextBytes) + "new"
	if _, err := ring.Write([]byte(input)); err != nil {
		t.Fatalf("write ring: %v", err)
	}
	output, dropped := ring.drain()
	if len(output) != maxToolTextBytes || !strings.HasSuffix(output, "new") || dropped != 3 {
		t.Fatalf("ring output = bytes:%d suffix:%v dropped:%d", len(output), strings.HasSuffix(output, "new"), dropped)
	}
	output, dropped = ring.drain()
	if output != "" || dropped != 0 {
		t.Fatalf("ring was not drained: %q, %d", output, dropped)
	}
}

// TestTerminateProcess verifies explicit process-group termination and removal.
func TestTerminateProcess(t *testing.T) {
	manager := newProcessManager(log.New(&bytes.Buffer{}, "", 0), time.Minute)
	t.Cleanup(manager.Close)
	started, err := manager.Start(startProcessInput{Mode: "shell", Command: "sleep 10"})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	result, err := manager.Terminate(started.SessionID)
	if err != nil {
		t.Fatalf("terminate process: %v", err)
	}
	if result.Status != "terminated" || result.ExitCode == 0 {
		t.Fatalf("terminate result = %#v", result)
	}
	if _, err = manager.Poll(started.SessionID); err == nil {
		t.Fatal("terminated session remained available")
	}
}

// TestProcessSessionExpiry verifies idle processes are killed and removed.
func TestProcessSessionExpiry(t *testing.T) {
	manager := newProcessManager(log.New(&bytes.Buffer{}, "", 0), 30*time.Millisecond)
	t.Cleanup(manager.Close)
	started, err := manager.Start(startProcessInput{Mode: "shell", Command: "sleep 10"})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err = manager.Poll(started.SessionID); err != nil {
			return
		}
		// Poll resets activity, so leave enough time for the next interval to expire.
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("idle process session did not expire")
}

// TestInteractiveCommandValidation verifies mutually exclusive mode inputs.
func TestInteractiveCommandValidation(t *testing.T) {
	tests := []startProcessInput{
		{},
		{Mode: "exec"},
		{Mode: "exec", Executable: "true", Command: "true"},
		{Mode: "shell"},
		{Mode: "shell", Command: "true", Args: []string{"bad"}},
	}
	for _, input := range tests {
		if _, err := interactiveCommand(input); err == nil {
			t.Fatalf("invalid input accepted: %#v", input)
		}
	}
}

// waitForProcessExit polls until a process reaches its final state.
func waitForProcessExit(t *testing.T, manager *processManager, sessionID string) processPollOutput {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var combined processPollOutput
	for time.Now().Before(deadline) {
		result, err := manager.Poll(sessionID)
		if err != nil {
			t.Fatalf("poll process: %v", err)
		}
		combined.Stdout += result.Stdout
		combined.Stderr += result.Stderr
		combined.StdoutDroppedBytes += result.StdoutDroppedBytes
		combined.StderrDroppedBytes += result.StderrDroppedBytes
		combined.Status = result.Status
		combined.ExitCode = result.ExitCode
		if result.Status != "running" {
			return combined
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %s did not exit", sessionID)
	return processPollOutput{}
}
