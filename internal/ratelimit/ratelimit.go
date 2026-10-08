// Package ratelimit throttles MCP requests per authenticated user.
package ratelimit

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// Config configures the per-user limiter.
type Config struct {
	// RequestsPerMinute is the sustained allowance for one user. Zero or a
	// negative value disables throttling.
	RequestsPerMinute int

	// Burst is the number of requests a user may issue back to back before
	// the sustained rate applies. An MCP client commonly fans out several
	// tool calls to answer one prompt, so a burst below that fan-out would
	// throttle normal use. Defaults to a quarter of RequestsPerMinute, with a
	// floor of 5.
	Burst int

	// IdleTTL is how long an unused limiter is retained before eviction.
	IdleTTL time.Duration

	Logger  *zerolog.Logger
	Metrics *observability.Metrics
}

// Limiter applies a token bucket per authenticated user.
//
// The limit is per task, not per cluster. With several tasks behind a load
// balancer the effective allowance for one user is the configured rate
// multiplied by the task count, because no state is shared between tasks. That
// is a deliberate trade: a shared counter would need a round trip to an
// external store on every request, and the purpose here is to stop one
// account from exhausting the Microsoft Graph throttling budget for everyone
// else, not to meter usage precisely. Graph's own per-mailbox throttling
// remains the authoritative limit.
type Limiter struct {
	requestsPerMinute int
	burst             int
	idleTTL           time.Duration
	logger            *zerolog.Logger
	metrics           *observability.Metrics

	mu      sync.Mutex
	buckets map[string]*bucket
	// lastSweep bounds how often eviction walks the map.
	lastSweep time.Time
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New builds a Limiter. A nil result means throttling is disabled.
func New(cfg Config) *Limiter {
	if cfg.RequestsPerMinute <= 0 {
		return nil
	}

	// An explicit burst is honoured as given. The floor applies only to the
	// derived default, so that a small sustained rate still admits one MCP
	// prompt's worth of tool calls.
	burst := cfg.Burst
	if burst <= 0 {
		burst = cfg.RequestsPerMinute / 4
		if burst < 5 {
			burst = 5
		}
	}

	idleTTL := cfg.IdleTTL
	if idleTTL <= 0 {
		idleTTL = 15 * time.Minute
	}

	return &Limiter{
		requestsPerMinute: cfg.RequestsPerMinute,
		burst:             burst,
		idleTTL:           idleTTL,
		logger:            cfg.Logger,
		metrics:           cfg.Metrics,
		buckets:           make(map[string]*bucket),
		lastSweep:         time.Now(),
	}
}

// Handler wraps next with per-user throttling. It must be mounted inside the
// authentication middleware, because the user identity it keys on is placed in
// the context there. An unauthenticated request is rejected before it reaches
// this handler, so there is no anonymous bucket to exhaust.
func (l *Limiter) Handler(next http.Handler) http.Handler {
	if l == nil {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := observability.GetUserID(r.Context())
		if userID == "" {
			// Fail closed. An empty identity would otherwise put every such
			// request into one shared bucket, which is both a shared denial
			// of service and a way to opt out of throttling.
			l.reject(w, r, "unidentified", 0)
			return
		}

		reservation := l.limiterFor(userID).Reserve()
		if !reservation.OK() {
			l.reject(w, r, userID, time.Minute)
			return
		}
		if delay := reservation.Delay(); delay > 0 {
			// Do not hold the request open waiting for a token. A queued MCP
			// request consumes a connection on the load balancer and a
			// goroutine here, so a throttled client would be able to pin
			// resources precisely by exceeding its limit.
			reservation.Cancel()
			l.reject(w, r, userID, delay)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// limiterFor returns the bucket for a user, creating it on first use.
func (l *Limiter) limiterFor(userID string) *rate.Limiter {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	if b, ok := l.buckets[userID]; ok {
		b.lastSeen = now
		return b.limiter
	}

	limiter := rate.NewLimiter(rate.Limit(float64(l.requestsPerMinute)/60.0), l.burst)
	l.buckets[userID] = &bucket{limiter: limiter, lastSeen: now}
	return limiter
}

// sweepLocked evicts idle buckets. Without eviction the map grows once per
// distinct user identity for the lifetime of the task, which is an unbounded
// allocation driven by request content.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	l.lastSweep = now

	for userID, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.idleTTL {
			delete(l.buckets, userID)
		}
	}
}

// reject writes 429 with a Retry-After hint.
func (l *Limiter) reject(w http.ResponseWriter, r *http.Request, userID string, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}

	w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)

	// JSON-RPC shaped so an MCP client surfaces the reason rather than a bare
	// transport failure. -32005 matches the internal rate limit error code.
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32005,
			"message": fmt.Sprintf("Rate limit exceeded. Retry in %d second(s).", seconds),
		},
	})

	if l.metrics != nil {
		l.metrics.RateLimitThrottled.WithLabelValues(r.URL.Path).Inc()
	}
	if l.logger != nil {
		l.logger.Warn().
			Str("user_id", userID).
			Int("retry_after_seconds", seconds).
			Int("requests_per_minute", l.requestsPerMinute).
			Msg("Request throttled")
	}
}
