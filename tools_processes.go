package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultSessionIdleTimeout = 30 * time.Minute

// startProcessInput describes a direct or Bash-backed interactive process.
type startProcessInput struct {
	Mode       string   `json:"mode" jsonschema:"exec for direct execution or shell for bash -lc"`
	Executable string   `json:"executable,omitempty" jsonschema:"executable required in exec mode"`
	Args       []string `json:"args,omitempty" jsonschema:"arguments used in exec mode"`
	Command    string   `json:"command,omitempty" jsonschema:"command string required in shell mode"`
	Cwd        string   `json:"cwd,omitempty" jsonschema:"working directory, defaults to the server current directory"`
}

// startProcessOutput identifies a newly running process session.
type startProcessOutput struct {
	SessionID string `json:"sessionId" jsonschema:"opaque interactive process session identifier"`
	Status    string `json:"status" jsonschema:"current process status"`
}

// writeStdinInput describes input sent to an interactive process.
type writeStdinInput struct {
	SessionID string `json:"sessionId" jsonschema:"interactive process session identifier"`
	Input     string `json:"input" jsonschema:"bytes to write to process stdin"`
	Close     bool   `json:"close,omitempty" jsonschema:"close stdin after writing"`
}

// writeStdinOutput reports a completed process input operation.
type writeStdinOutput struct {
	Written int    `json:"written" jsonschema:"number of input bytes written"`
	Status  string `json:"status" jsonschema:"current process status"`
}

// processSessionInput identifies an existing interactive process.
type processSessionInput struct {
	SessionID string `json:"sessionId" jsonschema:"interactive process session identifier"`
}

// processPollOutput reports unread output and current process state.
type processPollOutput struct {
	Stdout             string `json:"stdout" jsonschema:"unread standard output, drained by this poll"`
	Stderr             string `json:"stderr" jsonschema:"unread standard error, drained by this poll"`
	StdoutDroppedBytes int64  `json:"stdoutDroppedBytes" jsonschema:"older unread stdout bytes overwritten before this poll"`
	StderrDroppedBytes int64  `json:"stderrDroppedBytes" jsonschema:"older unread stderr bytes overwritten before this poll"`
	Status             string `json:"status" jsonschema:"running, exited, terminated, or expired"`
	ExitCode           int    `json:"exitCode" jsonschema:"process exit code, or -1 while running"`
}

// outputRing retains the newest unread process bytes up to the fixed cap.
type outputRing struct {
	mu      sync.Mutex
	data    []byte
	dropped int64
}

// Write implements io.Writer and overwrites the oldest unread bytes on overflow.
func (r *outputRing) Write(input []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	written := len(input)
	if len(input) >= maxToolTextBytes {
		r.dropped += int64(len(r.data) + len(input) - maxToolTextBytes)
		r.data = append(r.data[:0], input[len(input)-maxToolTextBytes:]...)
		return written, nil
	}
	overflow := len(r.data) + len(input) - maxToolTextBytes
	if overflow > 0 {
		r.dropped += int64(overflow)
		copy(r.data, r.data[overflow:])
		r.data = r.data[:len(r.data)-overflow]
	}
	r.data = append(r.data, input...)
	return written, nil
}

// drain returns and clears unread output and its dropped-byte count.
func (r *outputRing) drain() (string, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := strings.ToValidUTF8(string(r.data), "�")
	if len(value) > maxToolTextBytes {
		limit := maxToolTextBytes
		for limit > 0 && !utf8.ValidString(value[:limit]) {
			limit--
		}
		value = value[:limit]
	}
	dropped := r.dropped
	r.data = r.data[:0]
	r.dropped = 0
	return value, dropped
}

// processSession owns one process and its durable cross-request state.
type processSession struct {
	id     string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout outputRing
	stderr outputRing
	done   chan struct{}

	mu           sync.Mutex
	stdinMu      sync.Mutex
	status       string
	exitCode     int
	lastActivity time.Time
	stopOnce     sync.Once
}

// processManager owns interactive sessions and their inactivity cleanup loop.
type processManager struct {
	logger      *log.Logger
	idleTimeout time.Duration
	done        chan struct{}
	closeOnce   sync.Once

	mu       sync.Mutex
	sessions map[string]*processSession
	closed   bool
}

// newProcessManager starts a manager using the configured inactivity timeout.
func newProcessManager(logger *log.Logger, idleTimeout time.Duration) *processManager {
	manager := &processManager{
		logger: logger, idleTimeout: idleTimeout, done: make(chan struct{}),
		sessions: make(map[string]*processSession),
	}
	go manager.cleanupLoop()
	return manager
}

// registerProcessTools adds non-PTY interactive process tools.
func registerProcessTools(server *mcp.Server, manager *processManager) {
	mcp.AddTool(server, &mcp.Tool{Name: "start_process", Description: "Start a non-PTY direct or Bash-backed interactive process."},
		func(_ context.Context, _ *mcp.CallToolRequest, input startProcessInput) (*mcp.CallToolResult, startProcessOutput, error) {
			output, err := manager.Start(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "write_stdin", Description: "Write to an interactive process and optionally close stdin."},
		func(_ context.Context, _ *mcp.CallToolRequest, input writeStdinInput) (*mcp.CallToolResult, writeStdinOutput, error) {
			output, err := manager.WriteStdin(input)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "poll_process", Description: "Drain unread output and report interactive process status."},
		func(_ context.Context, _ *mcp.CallToolRequest, input processSessionInput) (*mcp.CallToolResult, processPollOutput, error) {
			output, err := manager.Poll(input.SessionID)
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "terminate_process", Description: "Terminate an interactive process and close its session."},
		func(_ context.Context, _ *mcp.CallToolRequest, input processSessionInput) (*mcp.CallToolResult, processPollOutput, error) {
			output, err := manager.Terminate(input.SessionID)
			return nil, output, err
		})
}

// Start launches a new process and stores its interactive session.
func (m *processManager) Start(input startProcessInput) (startProcessOutput, error) {
	command, err := interactiveCommand(input)
	if err != nil {
		return startProcessOutput{}, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return startProcessOutput{}, fmt.Errorf("open stdin: %w", err)
	}
	sessionID, err := newSessionID()
	if err != nil {
		_ = stdin.Close()
		return startProcessOutput{}, err
	}
	session := &processSession{
		id: sessionID, cmd: command, stdin: stdin, done: make(chan struct{}),
		status: "running", exitCode: -1, lastActivity: time.Now(),
	}
	command.Stdout = &session.stdout
	command.Stderr = &session.stderr

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = stdin.Close()
		return startProcessOutput{}, errors.New("process manager is shutting down")
	}
	if err = command.Start(); err != nil {
		m.mu.Unlock()
		_ = stdin.Close()
		return startProcessOutput{}, fmt.Errorf("start process: %w", err)
	}
	m.sessions[sessionID] = session
	m.mu.Unlock()

	m.logger.Printf("process_start session_id=%s mode=%s pid=%d", sessionID, input.Mode, command.Process.Pid)
	go m.wait(session)
	return startProcessOutput{SessionID: sessionID, Status: "running"}, nil
}

// WriteStdin writes exact input bytes and optionally closes process stdin.
func (m *processManager) WriteStdin(input writeStdinInput) (writeStdinOutput, error) {
	session, err := m.get(input.SessionID)
	if err != nil {
		return writeStdinOutput{}, err
	}
	session.mu.Lock()
	session.lastActivity = time.Now()
	if session.status != "running" {
		status := session.status
		session.mu.Unlock()
		return writeStdinOutput{}, fmt.Errorf("process is %s", status)
	}
	status := session.status
	session.mu.Unlock()
	session.stdinMu.Lock()
	defer session.stdinMu.Unlock()
	written, writeErr := io.WriteString(session.stdin, input.Input)
	if input.Close {
		if closeErr := session.stdin.Close(); writeErr == nil {
			writeErr = closeErr
		}
	}
	if writeErr != nil {
		return writeStdinOutput{}, fmt.Errorf("write stdin: %w", writeErr)
	}
	return writeStdinOutput{Written: written, Status: status}, nil
}

// Poll drains unread output and reports the current process state.
func (m *processManager) Poll(sessionID string) (processPollOutput, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return processPollOutput{}, err
	}
	session.mu.Lock()
	session.lastActivity = time.Now()
	status := session.status
	exitCode := session.exitCode
	session.mu.Unlock()
	stdout, stdoutDropped := session.stdout.drain()
	stderr, stderrDropped := session.stderr.drain()
	return processPollOutput{
		Stdout: stdout, Stderr: stderr, StdoutDroppedBytes: stdoutDropped,
		StderrDroppedBytes: stderrDropped, Status: status, ExitCode: exitCode,
	}, nil
}

// Terminate kills a process group, drains output, and removes its session.
func (m *processManager) Terminate(sessionID string) (processPollOutput, error) {
	session, err := m.get(sessionID)
	if err != nil {
		return processPollOutput{}, err
	}
	m.stop(session, "terminated")
	<-session.done
	output := pollSession(session)
	m.mu.Lock()
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	return output, nil
}

// pollSession drains one session without requiring manager membership.
func pollSession(session *processSession) processPollOutput {
	session.mu.Lock()
	status := session.status
	exitCode := session.exitCode
	session.mu.Unlock()
	stdout, stdoutDropped := session.stdout.drain()
	stderr, stderrDropped := session.stderr.drain()
	return processPollOutput{
		Stdout: stdout, Stderr: stderr, StdoutDroppedBytes: stdoutDropped,
		StderrDroppedBytes: stderrDropped, Status: status, ExitCode: exitCode,
	}
}

// Close terminates every process and stops inactivity cleanup.
func (m *processManager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		sessions := make([]*processSession, 0, len(m.sessions))
		for _, session := range m.sessions {
			sessions = append(sessions, session)
		}
		m.mu.Unlock()
		close(m.done)
		for _, session := range sessions {
			m.stop(session, "terminated")
		}
		for _, session := range sessions {
			<-session.done
		}
		m.mu.Lock()
		clear(m.sessions)
		m.mu.Unlock()
	})
}

// get returns a session by opaque identifier.
func (m *processManager) get(sessionID string) (*processSession, error) {
	if sessionID == "" {
		return nil, errors.New("sessionId is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return nil, errors.New("process session not found")
	}
	return session, nil
}

// wait records final state after the child process exits.
func (m *processManager) wait(session *processSession) {
	err := session.cmd.Wait()
	session.stdinMu.Lock()
	_ = session.stdin.Close()
	session.stdinMu.Unlock()
	session.mu.Lock()
	if session.status == "running" {
		session.status = "exited"
	}
	session.exitCode = commandExitCode(session.cmd.ProcessState)
	status := session.status
	exitCode := session.exitCode
	session.mu.Unlock()
	close(session.done)
	m.logger.Printf("process_finish session_id=%s status=%s exit_code=%d error=%v", session.id, status, exitCode, err)
}

// stop kills the session process group once and records the requested status.
func (m *processManager) stop(session *processSession, status string) {
	session.stopOnce.Do(func() {
		session.mu.Lock()
		if session.status == "running" {
			session.status = status
		}
		session.mu.Unlock()
		if session.cmd.Process != nil {
			err := syscall.Kill(-session.cmd.Process.Pid, syscall.SIGKILL)
			if err != nil && !errors.Is(err, syscall.ESRCH) {
				m.logger.Printf("process_kill_failed session_id=%s error=%v", session.id, err)
			}
		}
		session.stdinMu.Lock()
		_ = session.stdin.Close()
		session.stdinMu.Unlock()
	})
}

// cleanupLoop expires sessions that receive no poll or input activity.
func (m *processManager) cleanupLoop() {
	interval := m.idleTimeout / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case now := <-ticker.C:
			m.expireIdle(now)
		}
	}
}

// expireIdle removes and stops sessions beyond the inactivity threshold.
func (m *processManager) expireIdle(now time.Time) {
	m.mu.Lock()
	var expired []*processSession
	for id, session := range m.sessions {
		session.mu.Lock()
		idle := now.Sub(session.lastActivity) >= m.idleTimeout
		session.mu.Unlock()
		if idle {
			delete(m.sessions, id)
			expired = append(expired, session)
		}
	}
	m.mu.Unlock()
	for _, session := range expired {
		m.logger.Printf("process_expired session_id=%s", session.id)
		m.stop(session, "expired")
	}
}

// interactiveCommand validates mode-specific input and constructs its command.
func interactiveCommand(input startProcessInput) (*exec.Cmd, error) {
	var command *exec.Cmd
	switch input.Mode {
	case "exec":
		if input.Executable == "" {
			return nil, errors.New("executable is required in exec mode")
		}
		if input.Command != "" {
			return nil, errors.New("command is not valid in exec mode")
		}
		command = exec.Command(input.Executable, input.Args...)
	case "shell":
		if input.Command == "" {
			return nil, errors.New("command is required in shell mode")
		}
		if input.Executable != "" || len(input.Args) != 0 {
			return nil, errors.New("executable and args are not valid in shell mode")
		}
		command = exec.Command("bash", "-lc", input.Command)
	default:
		return nil, errors.New("mode must be exec or shell")
	}
	command.Dir = input.Cwd
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command, nil
}

// newSessionID returns an unpredictable identifier for cross-request lookup.
func newSessionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create session ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
