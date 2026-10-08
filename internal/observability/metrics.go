package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics
type Metrics struct {
	// MCP Server metrics
	MCPRequestDuration *prometheus.HistogramVec
	MCPRequestTotal    *prometheus.CounterVec
	MCPRequestErrors   *prometheus.CounterVec

	// MS Graph API metrics
	GraphAPICallDuration *prometheus.HistogramVec
	GraphAPICallTotal    *prometheus.CounterVec
	GraphAPICallErrors   *prometheus.CounterVec

	// Token validation metrics
	TokenValidationTotal   *prometheus.CounterVec
	TokenValidationErrors  *prometheus.CounterVec
	TokenValidationLatency *prometheus.HistogramVec

	// Cache metrics
	CacheHits   *prometheus.CounterVec
	CacheMisses *prometheus.CounterVec

	// Circuit breaker metrics
	CircuitBreakerState *prometheus.GaugeVec

	// Rate limiting metrics
	RateLimitThrottled *prometheus.CounterVec

	// Attachment extraction metrics
	AttachmentExtractionTotal   *prometheus.CounterVec
	AttachmentExtractionLatency *prometheus.HistogramVec
}

// NewMetrics creates and registers all Prometheus metrics
func NewMetrics() *Metrics {
	return &Metrics{
		// MCP Server metrics
		MCPRequestDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "mcp_request_duration_seconds",
				Help:    "Duration of MCP requests in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "status"},
		),
		MCPRequestTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "mcp_request_total",
				Help: "Total number of MCP requests",
			},
			[]string{"method", "status"},
		),
		MCPRequestErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "mcp_request_errors_total",
				Help: "Total number of MCP request errors",
			},
			[]string{"method", "error_code"},
		),

		// MS Graph API metrics
		GraphAPICallDuration: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "graph_api_call_duration_seconds",
				Help:    "Duration of MS Graph API calls in seconds",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"endpoint", "status"},
		),
		GraphAPICallTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "graph_api_call_total",
				Help: "Total number of MS Graph API calls",
			},
			[]string{"endpoint", "status"},
		),
		GraphAPICallErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "graph_api_call_errors_total",
				Help: "Total number of MS Graph API call errors",
			},
			[]string{"endpoint", "error_type"},
		),

		// Token validation metrics
		TokenValidationTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "token_validation_total",
				Help: "Total number of token validations",
			},
			[]string{"status"},
		),
		TokenValidationErrors: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "token_validation_errors_total",
				Help: "Total number of token validation errors",
			},
			[]string{"error_type"},
		),
		TokenValidationLatency: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "token_validation_latency_seconds",
				Help:    "Token validation latency in seconds",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
			},
			[]string{"cached"},
		),

		// Cache metrics
		CacheHits: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_hits_total",
				Help: "Total number of cache hits",
			},
			[]string{"cache_type"},
		),
		CacheMisses: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "cache_misses_total",
				Help: "Total number of cache misses",
			},
			[]string{"cache_type"},
		),

		// Circuit breaker metrics
		CircuitBreakerState: promauto.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "circuit_breaker_state",
				Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
			},
			[]string{"service"},
		),

		// Rate limiting metrics
		RateLimitThrottled: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rate_limit_throttled_total",
				Help: "Requests rejected by the per-user rate limiter",
			},
			[]string{"path"},
		),

		// Attachment extraction metrics
		AttachmentExtractionTotal: promauto.NewCounterVec(
			prometheus.CounterOpts{
				Name: "attachment_extraction_total",
				Help: "Total number of email attachments processed by the markdown extractor, by kind and status.",
			},
			[]string{"kind", "status"},
		),
		AttachmentExtractionLatency: promauto.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "attachment_extraction_latency_seconds",
				Help:    "Latency of email attachment markdown extraction in seconds.",
				Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
			},
			[]string{"kind", "status"},
		),
	}
}
