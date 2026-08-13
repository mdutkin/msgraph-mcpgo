package gemini

import (
	"context"
	"fmt"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/rs/zerolog"
	"google.golang.org/genai"
)

// Agent wraps the Gemini API client
type Agent struct {
	client  *genai.Client
	model   string
	logger  *zerolog.Logger
	metrics *observability.Metrics
	timeout time.Duration
}

// NewAgent creates a new Gemini agent
func NewAgent(apiKey, modelName string, timeout time.Duration, logger *zerolog.Logger, metrics *observability.Metrics) (*Agent, error) {
	ctx := context.Background()

	// Create Gemini client
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	logger.Info().
		Str("model", modelName).
		Msg("Gemini agent initialized")

	return &Agent{
		client:  client,
		model:   modelName,
		logger:  logger,
		metrics: metrics,
		timeout: timeout,
	}, nil
}

// Close closes the Gemini client
func (a *Agent) Close() error {
	// Gemini client doesn't require explicit closing
	return nil
}

// GenerateContent generates content using the Gemini model
func (a *Agent) GenerateContent(ctx context.Context, prompt string) (string, error) {
	start := time.Now()
	operation := "generate_content"
	var status string
	var err error

	defer func() {
		duration := time.Since(start).Seconds()
		a.metrics.GeminiAPICallDuration.WithLabelValues(operation, status).Observe(duration)
		a.metrics.GeminiAPICallTotal.WithLabelValues(operation, status).Inc()

		if err != nil {
			errorType := "unknown"
			if appErr, ok := err.(*apperrors.AppError); ok {
				errorType = fmt.Sprintf("code_%d", appErr.Code)
			}
			a.metrics.GeminiAPICallErrors.WithLabelValues(operation, errorType).Inc()
		}
	}()

	// Create context with timeout
	timeoutCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	// Prepare content
	contents := []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				{Text: prompt},
			},
		},
	}

	// Prepare generation config
	config := &genai.GenerateContentConfig{
		Temperature:     genai.Ptr(float32(0.7)),
		TopP:            genai.Ptr(float32(0.9)),
		TopK:            genai.Ptr(float32(40)),
		MaxOutputTokens: 2048,
	}

	// Generate content
	resp, genErr := a.client.Models.GenerateContent(timeoutCtx, a.model, contents, config)
	if genErr != nil {
		status = "error"
		err = apperrors.NewGeminiAPIError(genErr, map[string]interface{}{
			"operation": operation,
		})
		a.logger.Error().
			Err(genErr).
			Str("operation", operation).
			Msg("Gemini API call failed")
		return "", err
	}

	// Extract text from response
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
		status = "error"
		err = apperrors.NewGeminiAPIError(
			fmt.Errorf("no content in response"),
			map[string]interface{}{"operation": operation},
		)
		return "", err
	}

	// Get the text from the first part
	var result string
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			result += part.Text
		}
	}

	if result == "" {
		status = "error"
		err = apperrors.NewGeminiAPIError(
			fmt.Errorf("empty response from model"),
			map[string]interface{}{"operation": operation},
		)
		return "", err
	}

	status = "success"
	a.logger.Debug().
		Str("operation", operation).
		Int("response_length", len(result)).
		Dur("duration", time.Since(start)).
		Msg("Gemini API call succeeded")

	return result, nil
}
