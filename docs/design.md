# Design

## MCP transport

Zauberbesen is a Go MCP server using Streamable HTTP. The MCP endpoint is
`/mcp`, matching the default HTTP transport selected by the llama.cpp web UI.
Legacy HTTP+SSE is not supported.

The Go module path is `github.com/matthias-p-nowak/zauberbesen-mcp`.

## Initial tool

The server exposes a parameterless `ping` tool. A successful call returns the
text `pong`.

## Operation

The server listens on `127.0.0.1:8080` by default. The `ZAUBERBESEN_ADDR`
environment variable may override the listen address. Each HTTP request is
logged to standard output after completion with its method, path, and response
status.

For direct browser access, the server reads the browser-supplied `Origin`
header. HTTP and HTTPS origins whose host is loopback or a private IP address
are allowed automatically. `ZAUBERBESEN_ALLOWED_ORIGIN` may name one exact
origin as an override, for example when the UI uses a local hostname. The
server answers allowed CORS preflight requests and permits the headers used by
MCP Streamable HTTP. Other origins are rejected rather than reflected.

## Filesystem tools

Zauberbesen applies no path allowlist or application-level permission checks.
Filesystem access is limited only by the container environment and the OS user
running the server.

The server provides these tools:

- `current_directory()` returns the server process's current working directory.
  Relative filesystem paths and omitted command working directories resolve
  from this directory.
- `list_files(path, recursive?, limit?)` lists one directory level by default
  or walks recursively when requested. Results include hidden entries and
  report each entry's path, type, and size. Every call is entry-limited:
  `limit` defaults to 1,000 and accepts values from 1 through 10,000.
- `grep(pattern, path, glob?, fixed?)` searches file contents. `pattern` is a
  regular expression by default; `fixed: true` selects literal matching.
- `read_file(path, startLine?, endLine?)` reads a complete file or a 1-based,
  inclusive line range and returns numbered lines.
- `edit_file(path, oldText, newText)` performs an exact replacement and fails
  unless `oldText` occurs exactly once.
- `write_file(path, content)` creates or replaces a complete file.
- `write_lines(path, startLine, endLine, newText)` replaces an existing,
  1-based inclusive line range. Content outside the range and the file's final
  newline state are preserved. Insertions use `edit_file` instead.

## Synchronous command tools

Zauberbesen applies no executable allowlist, shell filtering, path restriction,
or other application-level permission check. Commands inherit the permissions
and environment of the server process.

- `exec` launches an executable directly with an argument array.
- `shell` executes a command string through `bash -lc`.

Both tools accept an optional working directory, a one-shot stdin string, a
caller-overridable timeout, and an optional progress-notification mode. Stdin
is closed after the supplied string is written. The default timeout is 300
seconds and there is no application-level maximum. Cancelling the MCP request
cancels the command.

The server logs command startup immediately. The final result reports stdout,
stderr, and exit code separately. Synchronous execution remains the default;
when progress mode is requested, the server also attempts MCP progress
notifications while retaining the same final result contract.

## Interactive process sessions

Non-PTY interactive sessions support processes such as servers, REPLs, and
line-oriented prompts. Full-screen terminal applications are out of scope.

- `start_process` starts a session and returns its session ID. `mode: "exec"`
  accepts an executable and arguments; `mode: "shell"` accepts a command for
  `bash -lc`. Both modes accept an optional working directory.
- `write_stdin(sessionId, input)` writes input to a running session.
- `poll_process(sessionId)` returns unread stdout and stderr, process status,
  and the exit code once available. Returned output is drained.
- `terminate_process(sessionId)` stops the process and closes its session.

A running session expires after 30 minutes without `poll_process` or
`write_stdin`. The inactivity duration is configurable when the server starts.
Zauberbesen also terminates sessions during server shutdown.

Each session keeps separate 64 KiB ring buffers for unread stdout and stderr.
The newest output is retained; a poll reports `droppedBytes` when older unread
data was overwritten.

## Output limits

Ordinary textual tool results are limited to 64 KiB, with no caller override.
Truncated results report `truncated: true`. The command limit applies
separately to stdout and stderr. Truncated file listings and grep results also
report how many entries or matches were omitted.

