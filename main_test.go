package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestPingTool verifies the complete Streamable HTTP tool workflow.
func TestPingTool(t *testing.T) {
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	server := newMCPServer(logger)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(loggingMiddleware(logger, handler))
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.1.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: httpServer.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatalf("call ping: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "pong" {
		t.Fatalf("content = %#v, want text pong", result.Content[0])
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}
	httpServer.Close()
	if !strings.Contains(logs.String(), "tool=ping status=ok") {
		t.Fatalf("logs do not contain tool status: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "method=POST") || !strings.Contains(logs.String(), "status=200") {
		t.Fatalf("logs do not contain HTTP invocation status: %s", logs.String())
	}
}

// TestAddress verifies the documented default and environment override.
func TestAddress(t *testing.T) {
	t.Setenv("ZAUBERBESEN_ADDR", "")
	if got := address(); got != defaultAddress {
		t.Fatalf("default address = %q, want %q", got, defaultAddress)
	}
	t.Setenv("ZAUBERBESEN_ADDR", "127.0.0.1:9090")
	if got := address(); got != "127.0.0.1:9090" {
		t.Fatalf("configured address = %q", got)
	}
}

// TestCORSMiddleware verifies direct browser preflight handling.
func TestCORSMiddleware(t *testing.T) {
	const origin = "http://192.168.0.32:9931"
	request := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8080/mcp", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder := httptest.NewRecorder()

	corsMiddleware("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight reached MCP handler")
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("allowed origin = %q, want %q", got, origin)
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id" {
		t.Fatalf("exposed headers = %q", got)
	}
}

// TestAllowedOrigin verifies automatic local-origin detection and overrides.
func TestAllowedOrigin(t *testing.T) {
	tests := []struct {
		name       string
		origin     string
		configured string
		want       bool
	}{
		{name: "private IPv4", origin: "http://192.168.0.32:9931", want: true},
		{name: "loopback IPv4", origin: "http://127.0.0.1:9931", want: true},
		{name: "localhost", origin: "https://localhost:9931", want: true},
		{name: "public rejected", origin: "https://example.com", want: false},
		{name: "exact override", origin: "https://llama.lan", configured: "https://llama.lan", want: true},
		{name: "non-web scheme", origin: "file://192.168.0.32", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAllowedOrigin(test.origin, test.configured); got != test.want {
				t.Fatalf("isAllowedOrigin(%q, %q) = %v, want %v", test.origin, test.configured, got, test.want)
			}
		})
	}
}

// TestCORSMiddlewareRejectsOtherOrigins verifies origins are not reflected.
func TestCORSMiddlewareRejectsOtherOrigins(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/mcp", nil)
	request.Header.Set("Origin", "http://untrusted.example")
	recorder := httptest.NewRecorder()
	reached := false

	corsMiddleware("http://192.168.0.32:9931", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(recorder, request)

	if reached || recorder.Code != http.StatusForbidden {
		t.Fatalf("downstream reached = %v, status = %d", reached, recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected allowed origin %q", got)
	}
}
