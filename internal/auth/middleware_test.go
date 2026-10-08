package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/rs/zerolog"
)

func testLogger() *zerolog.Logger {
	l := zerolog.New(io.Discard)
	return &l
}

func newTestMiddleware(t *testing.T, mode ValidationMode) *Middleware {
	t.Helper()
	m, err := NewMiddleware(MiddlewareConfig{
		Inspector:           NewGraphTokenInspector(testTenantID),
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

func TestNewMiddlewareRequiresInspectorWhenInspecting(t *testing.T) {
	if _, err := NewMiddleware(MiddlewareConfig{Mode: ModeGraphPassthrough, Logger: testLogger()}); err == nil {
		t.Error("ModeGraphPassthrough accepted a nil inspector")
	}
	if _, err := NewMiddleware(MiddlewareConfig{Mode: ModeDisabled, Logger: testLogger()}); err != nil {
		t.Errorf("ModeDisabled rejected a nil inspector: %v", err)
	}
}

// In the production mode a credential that is not a usable Graph token is
// rejected before it reaches the transport.
func TestPassthroughModeRejectsNonGraphToken(t *testing.T) {
	m := newTestMiddleware(t, ModeGraphPassthrough)
	reached := false

	payload := graphPayload()
	payload["aud"] = "api://44444444-4444-4444-4444-444444444444"

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+makeToken(t, payload))

	rec := httptest.NewRecorder()
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if reached {
		t.Fatal("a non-Graph token reached the transport")
	}
}

// A valid forwarded Graph token reaches the transport with the caller's
// identity and credential in the context.
func TestPassthroughModeForwardsGraphToken(t *testing.T) {
	m := newTestMiddleware(t, ModeGraphPassthrough)
	token := makeToken(t, graphPayload())

	var seenToken string
	var seenUser string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken, _ = TokenFromContext(r.Context())
		seenUser = observability.GetUserID(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	m.Handler(next).ServeHTTP(httptest.NewRecorder(), req)

	if seenToken != token {
		t.Error("the credential was altered on the way to the transport")
	}
	if seenUser != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("user id not propagated: %q", seenUser)
	}
}

// ── verified_identity mode ───────────────────────────────────────────────────

func newVerifiedMiddleware(t *testing.T, entra *fakeEntra) *Middleware {
	t.Helper()

	m, err := NewMiddleware(MiddlewareConfig{
		Inspector: NewGraphTokenInspector(testTenantID),
		Verifier:  entra.verifier(t, time.Minute),
		Mode:      ModeVerifiedIdentity,
		Logger:    testLogger(),
	})
	if err != nil {
		t.Fatalf("NewMiddleware: %v", err)
	}
	return m
}

// graphTokenFor builds a forwarded Graph token for a given object id.
func graphTokenFor(t *testing.T, oid, tid string) string {
	t.Helper()
	payload := graphPayload()
	payload["oid"] = oid
	payload["tid"] = tid
	return makeToken(t, payload)
}

func TestVerifiedModeAcceptsBothCredentials(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	m := newVerifiedMiddleware(t, entra)

	graphToken := graphTokenFor(t, testOID, testTenantID)

	var forwarded string
	var recordedUser string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded, _ = TokenFromContext(r.Context())
		recordedUser = observability.GetUserID(r.Context())
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+graphToken)
	req.Header.Set(DefaultAssertionHeader, entra.assertion("key-1", nil))

	rec := httptest.NewRecorder()
	m.Handler(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	// The Graph credential must reach the transport untouched: it is the thing
	// that actually calls Microsoft Graph.
	if forwarded != graphToken {
		t.Error("the Graph token was altered on the way to the transport")
	}
	// The identity recorded must be the verified one.
	if recordedUser != testOID {
		t.Errorf("recorded user = %q, want the verified oid", recordedUser)
	}
}

func TestVerifiedModeRequiresTheAssertion(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	m := newVerifiedMiddleware(t, entra)

	reached := false
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+graphTokenFor(t, testOID, testTenantID))

	rec := httptest.NewRecorder()
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without an assertion, got %d", rec.Code)
	}
	if reached {
		t.Fatal("a request with no assertion reached the transport")
	}
	// The challenge should tell the operator which header is missing.
	if !strings.Contains(rec.Body.String(), DefaultAssertionHeader) {
		t.Errorf("response does not name the assertion header: %s", rec.Body.String())
	}
}

func TestVerifiedModeRejectsAForgedAssertion(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	entra.addKey("forged", false)
	m := newVerifiedMiddleware(t, entra)

	reached := false
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+graphTokenFor(t, testOID, testTenantID))
	req.Header.Set(DefaultAssertionHeader, entra.assertion("forged", nil))

	rec := httptest.NewRecorder()
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if reached {
		t.Fatal("a forged assertion reached the transport")
	}
}

// Pairing a valid assertion with another user's Graph token must be refused, so
// that a Graph call cannot be recorded under an identity that does not match
// the credential used to make it.
func TestVerifiedModeRejectsMismatchedCredentials(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	m := newVerifiedMiddleware(t, entra)

	otherUser := "99999999-9999-9999-9999-999999999999"

	reached := false
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+graphTokenFor(t, otherUser, testTenantID))
	req.Header.Set(DefaultAssertionHeader, entra.assertion("key-1", nil))

	rec := httptest.NewRecorder()
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for mismatched credentials, got %d", rec.Code)
	}
	if reached {
		t.Fatal("mismatched credentials reached the transport")
	}
}

// The Graph token is still inspected in verified mode: a valid assertion must
// not excuse a Graph token this server cannot use.
func TestVerifiedModeStillInspectsTheGraphToken(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	m := newVerifiedMiddleware(t, entra)

	payload := graphPayload()
	payload["aud"] = "api://44444444-4444-4444-4444-444444444444"

	reached := false
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+makeToken(t, payload))
	req.Header.Set(DefaultAssertionHeader, entra.assertion("key-1", nil))

	rec := httptest.NewRecorder()
	m.Handler(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if reached {
		t.Fatal("a non-Graph token reached the transport")
	}
}

func TestNewMiddlewareRequiresAVerifierInVerifiedMode(t *testing.T) {
	_, err := NewMiddleware(MiddlewareConfig{
		Inspector: NewGraphTokenInspector(testTenantID),
		Mode:      ModeVerifiedIdentity,
		Logger:    testLogger(),
	})
	if err == nil {
		t.Fatal("verified_identity mode was accepted with no verifier")
	}
}

func TestParseValidationMode(t *testing.T) {
	cases := map[string]ValidationMode{
		"verified_identity": ModeVerifiedIdentity,
		"graph_passthrough": ModeGraphPassthrough,
		"disabled":          ModeDisabled,
	}
	for in, want := range cases {
		got, err := ParseValidationMode(in)
		if err != nil || got != want {
			t.Errorf("ParseValidationMode(%q) = %v, %v", in, got, err)
		}
		if got.String() != in {
			t.Errorf("round trip failed: %q -> %q", in, got.String())
		}
	}
	if _, err := ParseValidationMode("whatever"); err == nil {
		t.Error("an unknown mode was accepted")
	}
}
