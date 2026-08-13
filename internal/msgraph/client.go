package msgraph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	abs "github.com/microsoft/kiota-abstractions-go"
	khttp "github.com/microsoft/kiota-http-go"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	msgraphcore "github.com/microsoftgraph/msgraph-sdk-go-core"
	"github.com/rs/zerolog"
	"github.com/sethvargo/go-retry"
	"github.com/sony/gobreaker"
)

// Client wraps the MS Graph SDK with retry and circuit breaker logic
type Client struct {
	token          string
	graphClient    *msgraphsdk.GraphServiceClient
	circuitBreaker *gobreaker.CircuitBreaker
	logger         *zerolog.Logger
	metrics        *observability.Metrics
	backoff        retry.Backoff
	maxRetries     uint64
	defaultTimeout time.Duration
}

// ClientConfig holds configuration for creating a Graph client
type ClientConfig struct {
	Token          string
	Logger         *zerolog.Logger
	Metrics        *observability.Metrics
	CircuitBreaker *gobreaker.CircuitBreaker
	Timeout        time.Duration
}

// NewClient creates a new MS Graph client
func NewClient(cfg ClientConfig) (*Client, error) {
	// Create authentication provider with the access token
	authProvider := &staticTokenProvider{token: cfg.Token}

	// Create Graph client options with timeout
	options := msgraphcore.GraphClientOptions{
		GraphServiceVersion:        "",
		GraphServiceLibraryVersion: "",
	}

	// Get HTTP client with default middleware (including URL replacement for /me endpoints),
	// then append our pathSyncHandler so it runs immediately before transport.RoundTrip.
	// This fixes an SDK bug where UrlReplaceHandler mutates req.URL.Path without updating
	// RawPath, causing Go's url.EscapedPath() to fall back to the un-encoded path
	// (e.g. sending "AAA=" instead of "AAA%3D" for Base64 message IDs).
	middlewares := msgraphcore.GetDefaultMiddlewaresWithOptions(&options)
	middlewares = append(middlewares, pathSyncHandler{})
	httpClient := khttp.GetDefaultClient(middlewares...)
	httpClient.Timeout = 0 // rely on context timeouts instead of a hard limit

	// Create Graph adapter with the properly configured HTTP client
	adapter, err := msgraphsdk.NewGraphRequestAdapterWithParseNodeFactoryAndSerializationWriterFactoryAndHttpClient(
		authProvider,
		nil,
		nil,
		httpClient,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create graph adapter: %w", err)
	}

	// Create Graph client
	graphClient := msgraphsdk.NewGraphServiceClient(adapter)

	return &Client{
		token:          cfg.Token,
		graphClient:    graphClient,
		circuitBreaker: cfg.CircuitBreaker,
		logger:         cfg.Logger,
		metrics:        cfg.Metrics,
		backoff:        retry.NewExponential(1 * time.Second),
		maxRetries:     3,
		defaultTimeout: cfg.Timeout,
	}, nil
}

// staticTokenProvider provides a static access token for authentication
type staticTokenProvider struct {
	token string
}

// AuthenticateRequest authenticates the request by adding the token
func (p *staticTokenProvider) AuthenticateRequest(ctx context.Context, request *abs.RequestInformation, additionalAuthenticationContext map[string]interface{}) error {
	if request == nil {
		return fmt.Errorf("request cannot be nil")
	}
	request.Headers.Add("Authorization", "Bearer "+p.token)
	return nil
}

// classifyGraphError inspects a Graph SDK error and wraps it into the correct
// AppError type so the circuit breaker and retry logic can make proper decisions.
//   - 429         → RateLimit   (retryable, trips breaker)
//   - 500-599     → GraphAPI    (retryable, trips breaker)
//   - 400-499     → ClientError (not retryable, does NOT trip breaker)
//   - unknown/nil → pass through unchanged
func classifyGraphError(err error, endpoint string) error {
	if err == nil {
		return nil
	}

	// Already classified as an AppError — leave it alone
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		return err
	}

	// Try to extract the HTTP status code from the Graph SDK error
	var apiErr *abs.ApiError
	if errors.As(err, &apiErr) {
		status := apiErr.ResponseStatusCode
		details := map[string]interface{}{
			"endpoint":    endpoint,
			"status_code": status,
		}

		switch {
		case status == 429:
			return apperrors.NewRateLimitError(fmt.Sprintf("rate limited on %s", endpoint))
		case status >= 500:
			return apperrors.NewGraphAPIError(err, details)
		case status >= 400:
			return apperrors.NewClientError(status, err, details)
		}
	}

	// Could not determine status — wrap as generic graph error so it's classified
	return apperrors.NewGraphAPIError(err, map[string]interface{}{"endpoint": endpoint})
}

// executeWithResilience wraps a Graph API call with circuit breaker and retry logic
func (c *Client) executeWithResilience(ctx context.Context, endpoint string, fn func() error) error {
	// Apply endpoint-specific timeouts
	timeout := c.defaultTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if endpoint == "transcript_content" {
		timeout = 90 * time.Second // Explicitly higher timeout for large VTT payloads
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var status string
	var err error

	defer func() {
		duration := time.Since(start).Seconds()
		c.metrics.GraphAPICallDuration.WithLabelValues(endpoint, status).Observe(duration)
		c.metrics.GraphAPICallTotal.WithLabelValues(endpoint, status).Inc()

		if err != nil {
			errorType := "unknown"
			if appErr, ok := err.(*apperrors.AppError); ok {
				errorType = fmt.Sprintf("code_%d", appErr.Code)
			}
			c.metrics.GraphAPICallErrors.WithLabelValues(endpoint, errorType).Inc()
		}
	}()

	// Execute with circuit breaker
	_, cbErr := c.circuitBreaker.Execute(func() (interface{}, error) {
		// Execute with retry logic
		retryErr := retry.Do(ctx, retry.WithMaxRetries(c.maxRetries, c.backoff), func(ctx context.Context) error {
			rawErr := fn()
			if rawErr == nil {
				return nil
			}

			// Classify the Graph SDK error into a proper AppError
			classified := classifyGraphError(rawErr, endpoint)

			if apperrors.IsRetryable(classified) {
				// Retryable error (rate limit, transient 5xx) — retry
				return retry.RetryableError(classified)
			}
			// Non-retryable error (404, 400, auth) — stop immediately
			return classified
		})
		return nil, retryErr
	})

	if cbErr != nil {
		// Check if circuit breaker is open
		if cbErr == gobreaker.ErrOpenState {
			status = "circuit_open"
			err = apperrors.NewCircuitOpenError("msgraph")
			c.logger.Warn().
				Str("endpoint", endpoint).
				Msg("Circuit breaker open for MS Graph")
			return err
		}

		if errors.Is(cbErr, context.Canceled) || errors.Is(cbErr, context.DeadlineExceeded) {
			status = "canceled"
			err = apperrors.NewGraphAPIError(cbErr, map[string]interface{}{
				"endpoint": endpoint,
			})
			c.logger.Warn().
				Err(cbErr).
				Str("endpoint", endpoint).
				Msg("Graph API call canceled or timed out")
			return err
		}

		status = "error"
		// Preserve the classified error if it's already an AppError
		var appErr *apperrors.AppError
		if errors.As(cbErr, &appErr) {
			err = cbErr
		} else {
			err = apperrors.NewGraphAPIError(cbErr, map[string]interface{}{
				"endpoint": endpoint,
			})
		}
		c.logger.Error().
			Err(cbErr).
			Str("endpoint", endpoint).
			Msg("Graph API call failed")
		return err
	}

	status = "success"
	c.logger.Debug().
		Str("endpoint", endpoint).
		Dur("duration", time.Since(start)).
		Msg("Graph API call succeeded")

	return nil
}
