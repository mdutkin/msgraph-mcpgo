package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func testLogger() *zerolog.Logger {
	l := zerolog.New(io.Discard)
	return &l
}

func newTestMiddleware(t *testing.T, mode ValidationMode) *Middleware {
	t.Helper()
	m, err := NewMiddleware(MiddlewareConfig{
		Mode:                mode,
		ResourceMetadataURL: "https://mcp.example.com/.well-known/oauth-protected-resource",
		Logger:              testLogger(),
	})
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}
	return m
}

func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// Every MCP method requires a credential. The discovery methods used to be
// exempt, which published the whole tool inventory to anonymous callers.
func TestDiscoveryMethodsAreNotExempt(t *testing.T) {
	bodies := map[string]string{
		"initialize":                `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		"tools/list":                `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"resources/list":            `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		"prompts/list":              `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`,
		"notifications/initialized": `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		"tools/call":                `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`,
	}

	// ModeDisabled accepts any bearer token, so a request carrying none is
	// rejected in every mode. Using the most permissive mode proves the
	// rejection comes from the absent credential and not from validation.
	m := newTestMiddleware(t, ModeDisabled)
	for method, body := range bodies {
		t.Run(method, func(t *testing.T) {
			reached := false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))

			m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			if reached {
				t.Fatal("unauthenticated request reached the MCP transport")
			}
		})
	}
}

// RFC 9728 section 5.1 and the MCP authorization specification both require
// the 401 to tell the client where to find resource metadata.
func TestRejectionAdvertisesResourceMetadata(t *testing.T) {
	m := newTestMiddleware(t, ModeDisabled)
	reached := false

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	challenge := rec.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		t.Fatalf("missing bearer challenge: %q", challenge)
	}
	if !strings.Contains(challenge, `resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`) {
		t.Fatalf("challenge omits resource_metadata: %q", challenge)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("401 response is cacheable")
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response body is not JSON: %v", err)
	}
	if body["error"] != "invalid_request" {
		t.Errorf("unexpected error code %q", body["error"])
	}
}

// A validation failure must not echo the expected audience or issuer back to
// the caller; that only assists someone probing the deployment.
func TestRejectionDoesNotDiscloseValidationDetail(t *testing.T) {
	m := newTestMiddleware(t, ModeDisabled)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	m.Handler(okHandler(new(bool))).ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, leak := range []string{"audience", "issuer", "api://", "login.microsoftonline.com"} {
		if strings.Contains(body, leak) {
			t.Errorf("response body discloses %q: %s", leak, body)
		}
	}
}

func TestAuthenticatedRequestReachesTransport(t *testing.T) {
	m := newTestMiddleware(t, ModeDisabled)
	reached := false

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !reached {
		t.Fatal("authenticated request did not reach the transport")
	}
}

// The downstream Graph call reads the credential from the context, so the
// middleware must put it there.
func TestTokenIsPlacedInContext(t *testing.T) {
	m := newTestMiddleware(t, ModeDisabled)
	const credential = "header.payload.signature"

	var seen string
	var present bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, present = TokenFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+credential)
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if !present || seen != credential {
		t.Fatalf("token not propagated: present=%v value=%q", present, seen)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{"valid", "Bearer abc.def.ghi", "abc.def.ghi", false},
		{"lowercase scheme", "bearer abc.def.ghi", "abc.def.ghi", false},
		{"mixed case scheme", "BeArEr abc.def.ghi", "abc.def.ghi", false},
		{"surrounding space", "Bearer   abc.def.ghi  ", "abc.def.ghi", false},
		{"missing header", "", "", true},
		{"wrong scheme", "Basic dXNlcjpwYXNz", "", true},
		{"scheme only", "Bearer", "", true},
		{"empty credential", "Bearer ", "", true},
		{"no scheme", "abc.def.ghi", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			got, err := bearerToken(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewMiddlewareRequiresValidatorWhenVerifying(t *testing.T) {
	for _, mode := range []ValidationMode{ModeVerify, ModeSkipSignature} {
		if _, err := NewMiddleware(MiddlewareConfig{Mode: mode, Logger: testLogger()}); err == nil {
			t.Errorf("mode %s accepted a nil validator", mode)
		}
	}
	if _, err := NewMiddleware(MiddlewareConfig{Mode: ModeDisabled, Logger: testLogger()}); err != nil {
		t.Errorf("ModeDisabled rejected a nil validator: %v", err)
	}
}
