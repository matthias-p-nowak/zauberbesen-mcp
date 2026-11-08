# Zauberbesen MCP

A small Go MCP server for the llama.cpp web UI. It exposes Streamable HTTP at
`/mcp`.

Available tools:

- `ping`
- `current_directory`
- `list_files`
- `grep`
- `read_file`
- `edit_file`
- `write_file`
- `write_lines`

Filesystem access is intentionally limited only by the container and the OS
user running Zauberbesen.

## Run

Run the server:

```sh
go run .
```

Add `http://127.0.0.1:8080/mcp` as an MCP server in the UI. The default bind
address is `127.0.0.1:8080`; set `ZAUBERBESEN_ADDR` to override it. Browser
origins on loopback and private IP addresses are accepted automatically. Set
`ZAUBERBESEN_ALLOWED_ORIGIN` only when an exact additional origin is needed.

## Verify

```sh
go test ./...
```
