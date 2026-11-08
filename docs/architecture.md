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

## Verification

Tests connect an MCP client to a temporary Streamable HTTP server and call the
`ping` tool end to end. Separate tests cover listen-address selection, CORS
preflight headers, and rejection of unconfigured origins.

Filesystem tests cover current-directory reporting, hidden, recursive, and
entry-limited listings, regex/fixed/glob search, ranged and truncated reads,
exact replacement failure, whole-file creation, inclusive line replacement,
deletion, and final-newline preservation.


