package httpmw

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func discardLogger() *zerolog.Logger {
	l := zerolog.New(io.Discard)
	return &l
}

// readBodyHandler reports how much it managed to read, and whether the read
// failed, which is how an over-limit body surfaces to the wrapped handler.
func readBodyHandler(read *int, readErr *error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		*read = len(body)
		*readErr = err
	})
}

func TestBodyWithinLimitPassesThrough(t *testing.T) {
	var read int
	var readErr error
	h := LimitRequestBody(1024, discardLogger(), readBodyHandler(&read, &readErr))

	payload := strings.Repeat("a", 512)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil {
		t.Fatalf("unexpected read error: %v", readErr)
	}
	if read != len(payload) {
		t.Fatalf("read %d bytes, want %d", read, len(payload))
	}
}

// A client that declares an oversized body is rejected before the body is
// read at all.
func TestDeclaredOversizedBodyIsRejectedEarly(t *testing.T) {
	reached := false
	h := LimitRequestBody(1024, discardLogger(), http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) { reached = true }))

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("a", 4096)))
	req.ContentLength = 4096

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", rec.Code)
	}
	if reached {
		t.Fatal("oversized request reached the wrapped handler")
	}
	if !strings.Contains(rec.Body.String(), "maxBytes") {
		t.Errorf("response does not report the limit: %s", rec.Body.String())
	}
}

// A client that lies about or omits Content-Length must still be stopped at
// the boundary, which is what actually enforces the bound.
func TestUndeclaredOversizedBodyIsStoppedAtTheBoundary(t *testing.T) {
	var read int
	var readErr error
	h := LimitRequestBody(1024, discardLogger(), readBodyHandler(&read, &readErr))

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(bytes.Repeat([]byte("a"), 8192)))
	req.ContentLength = -1

	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr == nil {
		t.Fatal("an oversized body was read without error")
	}
	if read > 1024 {
		t.Fatalf("read %d bytes past the 1024 byte limit", read)
	}
}

func TestNonPositiveLimitFallsBackToTheDefault(t *testing.T) {
	var read int
	var readErr error
	h := LimitRequestBody(0, discardLogger(), readBodyHandler(&read, &readErr))

	payload := strings.Repeat("a", 2048)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if readErr != nil || read != len(payload) {
		t.Fatalf("a body well under the default was rejected: read=%d err=%v", read, readErr)
	}
}
