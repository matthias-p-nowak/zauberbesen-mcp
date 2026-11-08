// Package main provides the Zauberbesen MCP server executable.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultAddress = "127.0.0.1:8080"
	serverVersion  = "0.1.0"
)

// pingInput is the parameterless input accepted by the ping tool.
type pingInput struct{}

// responseRecorder records the HTTP status emitted by a wrapped handler.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records status before forwarding the response header.
func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write records an implicit successful status before forwarding the body.
func (r *responseRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

// Flush forwards streaming flushes to the underlying response writer.
func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// newMCPServer constructs the server and registers its tools.
func newMCPServer(logger *log.Logger, processManagers ...*processManager) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "zauberbesen",
		Version: serverVersion,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ping",
		Description: "Check that the Zauberbesen MCP server is responsive.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ pingInput) (*mcp.CallToolResult, any, error) {
		logger.Print("tool=ping status=ok")
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "pong"}},
		}, nil, nil
	})
	registerFilesystemTools(server)
	registerCommandTools(server, logger)
	if len(processManagers) > 0 && processManagers[0] != nil {
		registerProcessTools(server, processManagers[0])
	}

	return server
}

// loggingMiddleware logs every completed HTTP invocation and response status.
func loggingMiddleware(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, request)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Printf("method=%s path=%s status=%d duration=%s", request.Method, request.URL.Path, status, time.Since(started).Round(time.Millisecond))
	})
}

// isAllowedOrigin reports whether origin is an exact override or local web UI.
func isAllowedOrigin(origin, configuredOrigin string) bool {
	if origin == configuredOrigin && configuredOrigin != "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// corsMiddleware permits MCP browser requests from trusted local origins.
func corsMiddleware(configuredOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin != "" && !isAllowedOrigin(origin, configuredOrigin) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Last-Event-ID, Mcp-Protocol-Version, Mcp-Session-Id")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
			w.Header().Set("Vary", "Origin")
			if request.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, request)
	})
}

// address returns the configured listen address.
func address() string {
	if configured := os.Getenv("ZAUBERBESEN_ADDR"); configured != "" {
		return configured
	}
	return defaultAddress
}

// allowedOrigin returns the exact browser origin permitted to call the server.
func allowedOrigin() string {
	return os.Getenv("ZAUBERBESEN_ALLOWED_ORIGIN")
}

// sessionIdleTimeout returns the configured interactive-session inactivity limit.
func sessionIdleTimeout() (time.Duration, error) {
	configured := os.Getenv("ZAUBERBESEN_SESSION_IDLE_TIMEOUT")
	if configured == "" {
		return defaultSessionIdleTimeout, nil
	}
	duration, err := time.ParseDuration(configured)
	if err != nil || duration <= 0 {
		return 0, errors.New("ZAUBERBESEN_SESSION_IDLE_TIMEOUT must be a positive duration")
	}
	return duration, nil
}

// main starts the Streamable HTTP MCP server.
func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	idleTimeout, err := sessionIdleTimeout()
	if err != nil {
		logger.Fatal(err)
	}
	processManager := newProcessManager(logger, idleTimeout)
	server := newMCPServer(logger, processManager)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", loggingMiddleware(logger, corsMiddleware(allowedOrigin(), handler)))

	listenAddress := address()
	logger.Printf("zauberbesen listening on http://%s/mcp", listenAddress)
	httpServer := &http.Server{Addr: listenAddress, Handler: mux}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	select {
	case <-signalContext.Done():
		logger.Print("zauberbesen shutting down")
	case err = <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Printf("http server failed: %v", err)
		}
	}

	processManager.Close()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err = httpServer.Shutdown(shutdownContext); err != nil {
		logger.Printf("http shutdown failed: %v", err)
	}
}
