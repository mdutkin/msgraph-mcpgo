package ratelimit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/rs/zerolog"
)

func testLimiter(t *testing.T, cfg Config) *Limiter {
	t.Helper()
	logger := zerolog.New(io.Discard)
	cfg.Logger = &logger
	return New(cfg)
}

// request drives the limiter as a given user and reports the status code.
func request(h http.Handler, userID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if userID != "" {
		req = req.WithContext(observability.WithUserID(req.Context(), userID))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func passthrough() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestBurstIsAllowedThenThrottled(t *testing.T) {
	l := testLimiter(t, Config{RequestsPerMinute: 60, Burst: 3})
	h := l.Handler(passthrough())

	for i := 0; i < 3; i++ {
		if code := request(h, "user-a").Code; code != http.StatusOK {
			t.Fatalf("request %d within burst got %d", i+1, code)
		}
	}

	rec := request(h, "user-a")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after the burst, got %d", rec.Code)
	}
	retryAfter, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("missing or invalid Retry-After: %q", rec.Header().Get("Retry-After"))
	}
}

// One user exhausting their allowance must not affect another.
func TestBucketsAreIsolatedPerUser(t *testing.T) {
	l := testLimiter(t, Config{RequestsPerMinute: 60, Burst: 2})
	h := l.Handler(passthrough())

	for i := 0; i < 3; i++ {
		request(h, "noisy")
	}
	if code := request(h, "noisy").Code; code != http.StatusTooManyRequests {
		t.Fatalf("noisy user was not throttled, got %d", code)
	}

	if code := request(h, "quiet").Code; code != http.StatusOK {
		t.Fatalf("quiet user was throttled by another user's traffic, got %d", code)
	}
}

// A request with no identity must be rejected rather than share one bucket
// with every other unidentified request.
func TestUnidentifiedRequestIsRejected(t *testing.T) {
	l := testLimiter(t, Config{RequestsPerMinute: 60})
	reached := false
	h := l.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))

	if code := request(h, "").Code; code != http.StatusTooManyRequests {
		t.Fatalf("expected rejection, got %d", code)
	}
	if reached {
		t.Fatal("unidentified request reached the transport")
	}
}

func TestZeroRateDisablesThrottling(t *testing.T) {
	if l := testLimiter(t, Config{RequestsPerMinute: 0}); l != nil {
		t.Fatal("expected a nil Limiter when the rate is zero")
	}

	// A nil Limiter must still produce a working handler.
	var l *Limiter
	h := l.Handler(passthrough())
	for i := 0; i < 50; i++ {
		if code := request(h, "user-a").Code; code != http.StatusOK {
			t.Fatalf("disabled limiter threw %d", code)
		}
	}
}

func TestBurstFloorIsApplied(t *testing.T) {
	// A quarter of 4 is 1, below the floor of 5.
	l := testLimiter(t, Config{RequestsPerMinute: 4})
	if l.burst != 5 {
		t.Fatalf("expected the burst floor of 5, got %d", l.burst)
	}
}

// Idle buckets must be evicted, otherwise the map grows once per distinct
// identity for the lifetime of the task.
func TestIdleBucketsAreEvicted(t *testing.T) {
	l := testLimiter(t, Config{RequestsPerMinute: 60, IdleTTL: 20 * time.Millisecond})
	h := l.Handler(passthrough())

	request(h, "transient")
	if got := l.bucketCount(); got != 1 {
		t.Fatalf("expected 1 bucket, got %d", got)
	}

	time.Sleep(40 * time.Millisecond)
	request(h, "current")

	if got := l.bucketCount(); got != 1 {
		t.Fatalf("idle bucket was not evicted: %d buckets remain", got)
	}
}

func (l *Limiter) bucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
