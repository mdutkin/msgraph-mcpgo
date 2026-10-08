package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fnfbraga/msgraph-mcpgo/internal/auth"
	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/rs/zerolog"
)

// Prometheus collectors register against the default registerer, so the
// metric set must be built exactly once per test binary.
var (
	testMetricsOnce sync.Once
	testMetrics     *observability.Metrics
)

func sharedTestMetrics() *observability.Metrics {
	testMetricsOnce.Do(func() { testMetrics = observability.NewMetrics() })
	return testMetrics
}

// newTestServer builds a Server wired for transport-level tests. No Graph
// credential is configured, so only methods that need no Graph call are safe
// to exercise.
func newTestServer(t *testing.T) *Server {
	t.Helper()

	logger := zerolog.New(io.Discard)
	s, err := NewServer(ServerConfig{
		Logger:    &logger,
		Metrics:   sharedTestMetrics(),
		Stateless: true,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

// newAuthenticatedHandler mounts the transport behind the real authentication
// middleware, which is how it is mounted in cmd/server.
func newAuthenticatedHandler(t *testing.T, s *Server) http.Handler {
	t.Helper()

	logger := zerolog.New(io.Discard)
	m, err := auth.NewMiddleware(auth.MiddlewareConfig{
		Mode:                auth.ModeDisabled,
		ResourceMetadataURL: "https://mcp.example.test/.well-known/oauth-protected-resource",
		Logger:              &logger,
	})
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}
	return m.Handler(s.Handler())
}

func postMCP(t *testing.T, h http.Handler, body string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, DefaultEndpointPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if withToken {
		req.Header.Set("Authorization", "Bearer header.payload.signature")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
	`{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`

func TestTransportAnswersInitialize(t *testing.T) {
	h := newAuthenticatedHandler(t, newTestServer(t))

	rec := postMCP(t, h, initializeRequest, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeJSONRPC(t, rec, &resp)

	if resp.Error != nil {
		t.Fatalf("initialize returned an error: %s", resp.Error.Message)
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("unexpected jsonrpc version %q", resp.JSONRPC)
	}
	if resp.Result.ProtocolVersion == "" {
		t.Error("initialize did not negotiate a protocol version")
	}
	if resp.Result.ServerInfo.Name == "" {
		t.Error("initialize did not report server info")
	}
}

func TestTransportListsRegisteredTools(t *testing.T) {
	h := newAuthenticatedHandler(t, newTestServer(t))

	rec := postMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema struct {
					Type string `json:"type"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	decodeJSONRPC(t, rec, &resp)

	if len(resp.Result.Tools) != len(DefineMCPTools()) {
		t.Fatalf("expected %d tools, got %d", len(DefineMCPTools()), len(resp.Result.Tools))
	}
	for _, tool := range resp.Result.Tools {
		if tool.Name == "" {
			t.Error("tool advertised with no name")
		}
		if tool.InputSchema.Type != "object" {
			t.Errorf("tool %q advertised a malformed input schema", tool.Name)
		}
	}
}

// Guards the removal of the anonymous discovery path at the transport mount,
// not just in the middleware unit tests.
func TestTransportRejectsUnauthenticatedDiscovery(t *testing.T) {
	h := newAuthenticatedHandler(t, newTestServer(t))

	for _, body := range []string{
		initializeRequest,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
	} {
		rec := postMCP(t, h, body, false)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for %s, got %d", body, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("401 for %s carries no WWW-Authenticate challenge", body)
		}
	}
}

// A tool call with no Graph credential in context must fail closed rather than
// reaching Microsoft Graph unauthenticated.
func TestToolCallWithoutCredentialFailsClosed(t *testing.T) {
	s := newTestServer(t)

	// The transport is mounted directly, bypassing the middleware, to
	// simulate a request that never had a credential attached.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, DefaultEndpointPath,
		strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_users","arguments":{"query":"x"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	s.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "credential") && !strings.Contains(body, "error") {
		t.Fatalf("expected a failure, got %s", body)
	}
	if strings.Contains(body, "\"isError\":false") {
		t.Fatal("tool call succeeded without a credential")
	}
}

// decodeJSONRPC reads a transport response body, which may arrive as plain
// JSON or as a single SSE event depending on negotiation.
func decodeJSONRPC(t *testing.T, rec *httptest.ResponseRecorder, into any) {
	t.Helper()

	payload := rec.Body.String()
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(payload, "\n") {
			if after, ok := strings.CutPrefix(line, "data:"); ok {
				payload = strings.TrimSpace(after)
				break
			}
		}
	}

	if err := json.Unmarshal([]byte(payload), into); err != nil {
		t.Fatalf("response is not JSON-RPC: %v\nbody: %s", err, rec.Body.String())
	}
}
