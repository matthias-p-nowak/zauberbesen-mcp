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

