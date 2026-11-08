# Accepted requests

## Initial MCP server

- Provide a Go binary that serves MCP over HTTP for use by the llama.cpp web UI.
- Use MCP Streamable HTTP at `POST /mcp`.
- Provide a `ping` tool that returns `pong`.
- Log each HTTP invocation and its response status to standard output.
- Permit direct llama.cpp browser connections from loopback and private-network
  web origins without manual header configuration.

## Filesystem and process tools

- Provide filesystem discovery, content search, reading, and editing tools.
- Provide unrestricted direct and Bash-backed command execution; Docker and
  the server's OS user remain the permission boundary.
- Support both synchronous commands and non-PTY interactive process sessions.
- Bound tool output and clean up abandoned interactive sessions as specified in
  `docs/design.md`.

