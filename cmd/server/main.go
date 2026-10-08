package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/fnfbraga/msgraph-mcpgo/internal/auth"
	"github.com/fnfbraga/msgraph-mcpgo/internal/config"
	"github.com/fnfbraga/msgraph-mcpgo/internal/health"
	"github.com/fnfbraga/msgraph-mcpgo/internal/httpmw"
	"github.com/fnfbraga/msgraph-mcpgo/internal/mcp"
	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/fnfbraga/msgraph-mcpgo/internal/ratelimit"
	"github.com/fnfbraga/msgraph-mcpgo/internal/toolpolicy"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/sony/gobreaker"
)

// Build information, set through -ldflags at build time. Reported at startup
// and on the readiness endpoint so a running task can be tied back to a commit
// without inspecting the image.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Initialize logger
	logger := observability.NewLogger(cfg.LogLevel, cfg.IsDevelopment())
	log.Logger = logger

	logger.Info().
		Str("environment", cfg.Environment).
		Int("server_port", cfg.ServerPort).
		Int("metrics_port", cfg.MetricsPort).
		Str("public_url", cfg.PublicURL).
		Str("network_exposure", cfg.NetworkExposure).
		Str("auth_mode", cfg.AuthMode).
		Bool("mcp_stateless", cfg.MCPStateless).
		Str("version", version).
		Str("commit", commit).
		Msg("Starting MS Graph MCP Server")

	// Initialize metrics
	metrics := observability.NewMetrics()

	// The caller presents a Microsoft Graph access token. It is inspected for
	// shape, audience, tenant and expiry, then forwarded unchanged. No client
	// secret and no on-behalf-of exchange are involved, so this process holds
	// no credential and cannot act for a user who is not currently calling it.
	graphTokenInspector := auth.NewGraphTokenInspector(cfg.AzureTenantID)

	validationMode, err := auth.ParseValidationMode(cfg.AuthMode)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid authentication configuration")
	}

	// In verified_identity mode the caller also sends an Entra token that can
	// be verified, because the Graph token cannot be: Microsoft publishes no
	// signing keys for tokens issued to its own APIs. Verifying the assertion
	// is what makes the identity in the audit log and in the rate limit key
	// Entra's statement rather than the caller's.
	var identityVerifier *auth.IdentityVerifier
	if validationMode == auth.ModeVerifiedIdentity {
		// The cache refreshes lazily on access, so it owns no goroutine and
		// needs no lifetime beyond this call.
		jwksCache, issuer, err := auth.NewJWKSCache(context.Background(), auth.JWKSConfig{
			TenantID:           cfg.AzureTenantID,
			RefreshInterval:    cfg.JWKSRefreshInterval,
			MinRefreshInterval: cfg.JWKSMinRefreshInterval,
			DiscoveryURL:       cfg.EntraDiscoveryURL,
			Logger:             &logger,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to read the Entra signing keys")
		}

		identityVerifier, err = auth.NewIdentityVerifier(auth.IdentityVerifierConfig{
			JWKS:      jwksCache,
			TenantID:  cfg.AzureTenantID,
			Audiences: cfg.IdentityAudiences,
			Logger:    &logger,
		})
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create the identity verifier")
		}

		logger.Info().
			Str("issuer", issuer).
			Strs("accepted_audiences", cfg.IdentityAudiences).
			Str("assertion_header", cfg.IdentityAssertionHeader).
			Msg("Identity assertion verification enabled")
	}

	// Initialize circuit breaker for MS Graph
	circuitBreaker := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        "msgraph",
		MaxRequests: 5,                // Allow 5 probe requests in half-open state
		Interval:    2 * time.Minute,  // Longer counting window smooths burst failures
		Timeout:     30 * time.Second, // Longer cooldown before re-probing after tripping
		IsSuccessful: func(err error) bool {
			if err == nil {
				return true
			}
			// Don't count context cancellations from sibling failures as failures
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return true
			}
			// Don't count half-open rejections as new failures
			if err == gobreaker.ErrTooManyRequests {
				return true
			}
			// Don't count client-side errors (auth, bad params) as infrastructure failures
			if !apperrors.IsCircuitBreakerFailure(err) {
				return true
			}
			return false
		},
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			// Require strong evidence before tripping: 8+ requests with 80%+ failure rate
			return counts.Requests >= 8 && failureRatio >= 0.8
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			logger.Warn().
				Str("circuit_breaker", name).
				Str("from", from.String()).
				Str("to", to.String()).
				Msg("Circuit breaker state changed")

			// Update metrics
			var stateValue float64
			switch to {
			case gobreaker.StateClosed:
				stateValue = 0
			case gobreaker.StateHalfOpen:
				stateValue = 1
			case gobreaker.StateOpen:
				stateValue = 2
			}
			metrics.CircuitBreakerState.WithLabelValues(name).Set(stateValue)
		},
	})

	// Resolve which tools and resources this deployment exposes. Done before
	// the server is built so a bad policy file stops startup rather than
	// producing a server with the wrong surface.
	policyPath := cfg.ToolPolicyFile
	policyExplicit := policyPath != ""
	if !policyExplicit {
		policyPath = toolpolicy.DefaultFile
	}

	policy, err := toolpolicy.Load(policyPath, policyExplicit, mcp.KnownToolNames(), mcp.KnownResourceURIs())
	if err != nil {
		log.Fatal().Err(err).Str("tool_policy_file", policyPath).Msg("Failed to load tool policy")
	}
	logger.Info().
		Str("tool_policy_file", policyPath).
		Bool("tool_policy_present", policyExplicit).
		Msg("Tool policy resolved")

	// Initialize MCP server
	mcpServer, err := mcp.NewServer(mcp.ServerConfig{
		AttachmentExtractor: attachments.New(),
		CircuitBreaker:      circuitBreaker,
		Logger:              &logger,
		Metrics:             metrics,
		GraphTimeout:        cfg.GraphTimeout,
		Policy:              policy,
		Stateless:           cfg.MCPStateless,
		EndpointPath:        mcp.DefaultEndpointPath,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create MCP server")
	}

	// Authentication is applied to every MCP request, discovery included.
	resourceIdentifier := strings.TrimSuffix(cfg.PublicURL, "/")
	metadataPath := server.ProtectedResourceMetadataPath(resourceIdentifier)

	authMiddleware, err := auth.NewMiddleware(auth.MiddlewareConfig{
		Inspector:           graphTokenInspector,
		Verifier:            identityVerifier,
		AssertionHeader:     cfg.IdentityAssertionHeader,
		Mode:                validationMode,
		ResourceMetadataURL: resourceIdentifier + metadataPath,
		Logger:              &logger,
		Metrics:             metrics,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create authentication middleware")
	}

	// Initialize health handler
	healthHandler := health.NewHandler(&logger)

	// Setup HTTP server
	mux := http.NewServeMux()

	// Per-user throttling sits inside authentication, because it keys on the
	// identity the middleware establishes.
	userLimiter := ratelimit.New(ratelimit.Config{
		RequestsPerMinute: cfg.RateLimitPerUser,
		Burst:             cfg.RateLimitBurst,
		IdleTTL:           cfg.RateLimitIdleTTL,
		Logger:            &logger,
		Metrics:           metrics,
	})
	if userLimiter == nil {
		logger.Warn().Msg("Per-user rate limiting is disabled (RATE_LIMIT_PER_USER=0)")
	}

	// MCP endpoint. The Streamable HTTP transport handles POST, GET and
	// DELETE itself, so the mux must not filter by method.
	// Order matters. The body limit is outermost so an oversized payload is
	// stopped before any work is done on it; authentication comes next so no
	// unauthenticated request reaches the limiter or the transport; throttling
	// is innermost of the three because it keys on the authenticated identity.
	mux.Handle(mcp.DefaultEndpointPath, httpmw.LimitRequestBody(
		cfg.MaxRequestBytes, &logger,
		authMiddleware.Handler(userLimiter.Handler(mcpServer.Handler())),
	))

	// OAuth 2.0 Protected Resource Metadata (RFC 9728). This document is
	// intentionally public: it is how an MCP client that receives a 401
	// discovers which authorization server issues tokens for this resource.
	// It advertises endpoints and scopes only, never a credential.
	mux.Handle(metadataPath, server.NewProtectedResourceMetadataHandler(
		server.ProtectedResourceMetadataConfig{
			Resource: resourceIdentifier,
			AuthorizationServers: []string{
				fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", cfg.AzureTenantID),
			},
			// Clients obtain a Microsoft Graph token, not a token for this
			// server, so the advertised scope is a Graph scope. The delegated
			// permissions behind ".default" are whatever the calling
			// application's own registration has consented.
			ScopesSupported:        []string{"https://graph.microsoft.com/.default"},
			BearerMethodsSupported: []string{"header"},
			ResourceName:           "Microsoft Graph MCP Server",
		},
	))

	// Health check endpoints
	mux.HandleFunc("/health/live", healthHandler.LivenessHandler)
	mux.HandleFunc("/health/ready", healthHandler.ReadinessHandler)

	// The browser test console and its Entra device-code proxy are compiled in
	// only under the "devconsole" build tag. In a release build this call is a
	// no-op and the handlers do not exist in the binary.
	registerDevConsole(mux, cfg, &logger)

	// Create HTTP server
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.ServerPort),
		Handler:           mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	// Start metrics server
	metricsServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler:           promhttp.Handler(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}

	go func() {
		logger.Info().
			Int("port", cfg.MetricsPort).
			Msg("Starting metrics server")

		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error().Err(err).Msg("Metrics server error")
		}
	}()

	// Start HTTP server in a goroutine
	go func() {
		logger.Info().
			Int("port", cfg.ServerPort).
			Msg("Starting MCP HTTP server")

		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// Mark as ready
	healthHandler.SetReady(true)

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info().Msg("Shutting down server...")

	// Mark as not ready
	healthHandler.SetReady(false)

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown HTTP server
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("HTTP server shutdown error")
	}

	// Shutdown metrics server
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("Metrics server shutdown error")
	}

	logger.Info().Msg("Server stopped gracefully")
}
