# Architecture

## Process

The repository builds one Go executable from `main.go`. It uses the official
`github.com/modelcontextprotocol/go-sdk` and serves its
`StreamableHTTPHandler` through Go's `net/http` server.

## HTTP pipeline

`POST /mcp` and the related Streamable HTTP methods pass through response
logging and origin-validating CORS middleware before reaching the MCP SDK
handler. The CORS middleware parses the browser-supplied origin and allows
loopback/private IP hosts or the exact `ZAUBERBESEN_ALLOWED_ORIGIN` override.
It rejects other browser origins and handles allowed preflight requests
locally. The logging response writer preserves HTTP streaming by forwarding
`Flush`.

## Tools

`newMCPServer` creates the server and registers the parameterless `ping` tool.
Its handler returns one MCP text content item containing `pong` and records a
successful tool status on standard output.

`registerFilesystemTools` registers `current_directory`, `list_files`, `grep`,
`read_file`, `edit_file`, `write_file`, and `write_lines`. Their typed MCP
schemas and handlers live in `tools_files.go`. `current_directory` uses
`os.Getwd`; relative paths used by the other tools resolve from that directory.

Filesystem tools use paths directly and rely on OS/container permissions.
Recursive operations do not follow directory symlinks. Text reads and edits
require valid UTF-8. Existing-file writes preserve permission bits, while new
files use mode `0644`; parent directories must already exist.

Listings are deterministic and enforce a default 1,000-entry limit with a
10,000-entry maximum. Grep walks regular files, reports file and line numbers,
and uses Go regular expressions. Its optional glob uses Go filepath matching
against both the path relative to the search root and the basename.

The shared bounded-text accumulator retains complete result items up to 64 KiB
and reports truncation plus the number of omitted items. This applies to
listing entries, grep matches, and numbered read lines.

`registerCommandTools` registers synchronous `exec` and `shell` handlers from
`tools_commands.go`. Both normalize into a shared command runner. `exec` passes
an executable and argument array directly to `os/exec`; `shell` invokes
`bash -lc` with the supplied command.

The runner uses a five-minute default context timeout, accepts positive
caller overrides, and connects the supplied stdin string before closing stdin.
Commands run in a new Unix process group; cancellation or timeout kills the
group so shell descendants do not remain behind. Nonzero command exits are
normal tool results, while failure to start a process is a tool error.

Stdout and stderr use independent first-output buffers capped at 64 KiB and
have separate truncation flags. The final structured result includes both
streams, exit code, and timeout status. Command startup and completion are
logged. When progress is requested and the MCP caller supplied a progress
token, the handler sends best-effort startup, periodic running, and completion
notifications through the active MCP session.

## Verification

Tests connect an MCP client to a temporary Streamable HTTP server and call the
`ping` tool end to end. Separate tests cover listen-address selection, CORS
preflight headers, and rejection of unconfigured origins.

Filesystem tests cover current-directory reporting, hidden, recursive, and
entry-limited listings, regex/fixed/glob search, ranged and truncated reads,
exact replacement failure, whole-file creation, inclusive line replacement,
deletion, and final-newline preservation.

Command tests cover direct arguments, cwd, one-shot stdin, separate streams,
nonzero exits, independent truncation, timeout/process-group termination,
invalid timeouts, startup failures, and lifecycle logging. The complete suite
also passes under Go's race detector.

## Interactive process manager

`processManager` in `tools_processes.go` owns non-PTY sessions across MCP
requests and registers `start_process`, `write_stdin`, `poll_process`, and
`terminate_process`. Session IDs are random 128-bit values encoded as hex.
Direct mode uses `os/exec` without shell parsing; shell mode uses `bash -lc`.

Each process runs in a separate Unix process group and has pipe-backed stdin,
stdout, and stderr. `write_stdin` serializes writes and can close stdin after a
write. Polling atomically drains each output ring and returns process status and
exit code. Explicit termination kills the process group, waits for collection,
returns remaining output, and removes the session.

Stdout and stderr have independent 64 KiB newest-data ring buffers. Overflow
discards the oldest unread bytes and increments the dropped-byte count returned
by the next poll. Polling resets each buffer and its counter.

The manager's cleanup loop checks inactivity at a bounded interval. Sessions
default to a 30-minute idle timeout configured by
`ZAUBERBESEN_SESSION_IDLE_TIMEOUT`; polling or writing stdin resets activity.
Expired sessions are removed and killed. `Close` rejects new sessions, kills
all remaining process groups, and waits for them to exit.

The executable uses signal-aware HTTP lifecycle management. SIGINT or SIGTERM
closes the process manager and gracefully shuts down the HTTP server with a
10-second deadline.

Interactive tests cover direct and shell modes, stdin and EOF, output draining,
newest-data overflow accounting, termination, expiration, validation, and
configuration. The suite passes under Go's race detector.


