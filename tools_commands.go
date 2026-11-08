package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultCommandTimeout = 5 * time.Minute

// execInput describes direct executable invocation without shell parsing.
type execInput struct {
	Executable     string   `json:"executable" jsonschema:"executable name or path"`
	Args           []string `json:"args,omitempty" jsonschema:"arguments passed directly to the executable"`
	Cwd            string   `json:"cwd,omitempty" jsonschema:"working directory, defaults to the server current directory"`
	Stdin          string   `json:"stdin,omitempty" jsonschema:"one-shot standard input closed after writing"`
	TimeoutSeconds int64    `json:"timeoutSeconds,omitempty" jsonschema:"timeout in seconds, defaults to 300"`
	Progress       bool     `json:"progress,omitempty" jsonschema:"send MCP progress notifications when the client provides a progress token"`
}

// shellInput describes Bash command execution.
type shellInput struct {
	Command        string `json:"command" jsonschema:"command string passed to bash -lc"`
	Cwd            string `json:"cwd,omitempty" jsonschema:"working directory, defaults to the server current directory"`
	Stdin          string `json:"stdin,omitempty" jsonschema:"one-shot standard input closed after writing"`
	TimeoutSeconds int64  `json:"timeoutSeconds,omitempty" jsonschema:"timeout in seconds, defaults to 300"`
	Progress       bool   `json:"progress,omitempty" jsonschema:"send MCP progress notifications when the client provides a progress token"`
}

// commandOutput is the final result of a synchronous command.
type commandOutput struct {
	Stdout          string `json:"stdout" jsonschema:"captured standard output"`
	Stderr          string `json:"stderr" jsonschema:"captured standard error"`
	ExitCode        int    `json:"exitCode" jsonschema:"process exit code, or -1 when unavailable"`
	TimedOut        bool   `json:"timedOut" jsonschema:"whether the configured timeout expired"`
	StdoutTruncated bool   `json:"stdoutTruncated" jsonschema:"whether standard output exceeded 64 KiB"`
	StderrTruncated bool   `json:"stderrTruncated" jsonschema:"whether standard error exceeded 64 KiB"`
}

// commandSpec contains the normalized inputs used by the shared runner.
type commandSpec struct {
	name           string
	args           []string
	cwd            string
	stdin          string
	timeoutSeconds int64
	progress       bool
}

// cappedCommandBuffer retains the first 64 KiB written to one command stream.
type cappedCommandBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

// Write implements io.Writer while discarding bytes beyond the fixed cap.
func (b *cappedCommandBuffer) Write(data []byte) (int, error) {
	remaining := maxToolTextBytes - b.buffer.Len()
	if remaining > 0 {
		writeLength := len(data)
		if writeLength > remaining {
			writeLength = remaining
		}
		_, _ = b.buffer.Write(data[:writeLength])
	}
	if len(data) > remaining {
		b.truncated = true
	}
	return len(data), nil
}

// String returns valid UTF-8 suitable for an MCP JSON response.
func (b *cappedCommandBuffer) String() string {
	value := strings.ToValidUTF8(b.buffer.String(), "�")
	if len(value) <= maxToolTextBytes {
		return value
	}
	b.truncated = true
	limit := maxToolTextBytes
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

// progressReporter serializes optional MCP command progress notifications.
type progressReporter struct {
	enabled bool
	token   any
	request *mcp.CallToolRequest
	mu      sync.Mutex
	step    float64
}

// notify sends one best-effort progress notification.
func (r *progressReporter) notify(ctx context.Context, message string) {
	if !r.enabled || r.token == nil || r.request == nil || r.request.Session == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.step++
	_ = r.request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
		ProgressToken: r.token,
		Progress:      r.step,
		Message:       message,
	})
}

// registerCommandTools adds synchronous direct and Bash command tools.
func registerCommandTools(server *mcp.Server, logger *log.Logger) {
	mcp.AddTool(server, &mcp.Tool{Name: "exec", Description: "Execute a program directly without shell parsing."},
		func(ctx context.Context, request *mcp.CallToolRequest, input execInput) (*mcp.CallToolResult, commandOutput, error) {
			if input.Executable == "" {
				return nil, commandOutput{}, errors.New("executable is required")
			}
			output, err := runCommand(ctx, request, logger, commandSpec{
				name: input.Executable, args: input.Args, cwd: input.Cwd, stdin: input.Stdin,
				timeoutSeconds: input.TimeoutSeconds, progress: input.Progress,
			})
			return nil, output, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "shell", Description: "Execute a command string through bash -lc."},
		func(ctx context.Context, request *mcp.CallToolRequest, input shellInput) (*mcp.CallToolResult, commandOutput, error) {
			if input.Command == "" {
				return nil, commandOutput{}, errors.New("command is required")
			}
			output, err := runCommand(ctx, request, logger, commandSpec{
				name: "bash", args: []string{"-lc", input.Command}, cwd: input.Cwd, stdin: input.Stdin,
				timeoutSeconds: input.TimeoutSeconds, progress: input.Progress,
			})
			return nil, output, err
		})
}

// runCommand executes a normalized command and captures its final result.
func runCommand(parent context.Context, request *mcp.CallToolRequest, logger *log.Logger, spec commandSpec) (commandOutput, error) {
	timeout, err := commandTimeout(spec.timeoutSeconds)
	if err != nil {
		return commandOutput{}, err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	command := exec.CommandContext(ctx, spec.name, spec.args...)
	command.Dir = spec.cwd
	command.Stdin = strings.NewReader(spec.stdin)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}

	var stdout cappedCommandBuffer
	var stderr cappedCommandBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	reporter := progressReporter{enabled: spec.progress, request: request}
	if request != nil && request.Params != nil {
		reporter.token = request.Params.GetProgressToken()
	}
	logger.Printf("command_start executable=%q args=%q cwd=%q", spec.name, spec.args, spec.cwd)
	reporter.notify(ctx, "command started")

	done := make(chan struct{})
	go reportCommandProgress(ctx, &reporter, done)
	runErr := command.Run()
	close(done)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

	result := commandOutput{
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		ExitCode:        commandExitCode(command.ProcessState),
		TimedOut:        timedOut,
		StdoutTruncated: stdout.truncated,
		StderrTruncated: stderr.truncated,
	}
	reporter.notify(parent, "command completed")
	logger.Printf("command_finish executable=%q exit_code=%d timed_out=%t", spec.name, result.ExitCode, result.TimedOut)

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) || timedOut || errors.Is(parent.Err(), context.Canceled) {
			return result, nil
		}
		return commandOutput{}, fmt.Errorf("start command: %w", runErr)
	}
	return result, nil
}

// commandTimeout validates and converts a caller timeout or returns the default.
func commandTimeout(seconds int64) (time.Duration, error) {
	if seconds == 0 {
		return defaultCommandTimeout, nil
	}
	if seconds < 0 || seconds > math.MaxInt64/int64(time.Second) {
		return 0, errors.New("timeoutSeconds must be a positive duration")
	}
	return time.Duration(seconds) * time.Second, nil
}

// commandExitCode returns an unavailable sentinel when no process state exists.
func commandExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}

// reportCommandProgress emits periodic best-effort updates until completion.
func reportCommandProgress(ctx context.Context, reporter *progressReporter, done <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			reporter.notify(ctx, "command still running")
		}
	}
}
