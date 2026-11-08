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

## Verification

Tests connect an MCP client to a temporary Streamable HTTP server and call the
`ping` tool end to end. Separate tests cover listen-address selection, CORS
preflight headers, and rejection of unconfigured origins.


