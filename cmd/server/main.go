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
	"github.com/fnfbraga/msgraph-mcpgo/internal/mcp"
	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/mark3labs/mcp-go/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/sony/gobreaker"
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
		Bool("mcp_stateless", cfg.MCPStateless).
		Msg("Starting MS Graph MCP Server")

	// Initialize metrics
	metrics := observability.NewMetrics()

	// Initialize JWKS cache
	jwksCache := auth.NewJWKSCache(cfg.AzureTenantID, cfg.JWKSCacheTTL, &logger)

	// Initialize token validator
	tokenValidator := auth.NewTokenValidator(
		cfg.AzureClientID,
		cfg.AzureTenantID,
		jwksCache,
		&logger,
		metrics,
	)

	// Initialize OBO exchanger for delegated Graph access
	oboExchanger := auth.NewOBOExchanger(
		cfg.AzureTenantID,
		cfg.AzureClientID,
		cfg.AzureClientSecret,
		&logger,
	)

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

	// Initialize MCP server
	mcpServer, err := mcp.NewServer(mcp.ServerConfig{
		TokenValidator:      tokenValidator,
		OBOExchanger:        oboExchanger,
		AttachmentExtractor: attachments.New(),
		CircuitBreaker:      circuitBreaker,
		Logger:              &logger,
		Metrics:             metrics,
		GraphTimeout:        cfg.GraphTimeout,
		DisableAuth:         cfg.DisableAuth,
		SkipTokenValidation: cfg.SkipTokenValidation,
		Stateless:           cfg.MCPStateless,
		EndpointPath:        mcp.DefaultEndpointPath,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create MCP server")
	}

	// Authentication is applied to every MCP request, discovery included.
	validationMode := auth.ModeVerify
	switch {
	case cfg.DisableAuth:
		validationMode = auth.ModeDisabled
	case cfg.SkipTokenValidation:
		validationMode = auth.ModeSkipSignature
	}

	resourceIdentifier := strings.TrimSuffix(cfg.PublicURL, "/")
	metadataPath := server.ProtectedResourceMetadataPath(resourceIdentifier)

	authMiddleware, err := auth.NewMiddleware(auth.MiddlewareConfig{
		Validator:           tokenValidator,
		Mode:                validationMode,
		ResourceMetadataURL: resourceIdentifier + metadataPath,
		Logger:              &logger,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create authentication middleware")
	}

	// Initialize health handler
	healthHandler := health.NewHandler(&logger)

	// Setup HTTP server
	mux := http.NewServeMux()

	// MCP endpoint. The Streamable HTTP transport handles POST, GET and
	// DELETE itself, so the mux must not filter by method.
	mux.Handle(mcp.DefaultEndpointPath, authMiddleware.Handler(mcpServer.Handler()))

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
			ScopesSupported:        []string{fmt.Sprintf("api://%s/access_as_user", cfg.AzureClientID)},
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
		Addr:         fmt.Sprintf(":%d", cfg.ServerPort),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start metrics server
	metricsServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: promhttp.Handler(),
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
