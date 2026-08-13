package observability

import (
	"context"
	"io"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ContextKey is the type for context keys
type ContextKey string

const (
	// CorrelationIDKey is the context key for correlation ID
	CorrelationIDKey ContextKey = "correlation_id"
	// UserIDKey is the context key for user ID
	UserIDKey ContextKey = "user_id"
)

// NewLogger creates a new configured logger
func NewLogger(level string, isDevelopment bool) zerolog.Logger {
	// Parse log level
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		logLevel = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(logLevel)

	// Configure output format
	var output io.Writer
	if isDevelopment {
		// Pretty console output for development
		output = zerolog.ConsoleWriter{Out: os.Stdout}
	} else {
		// JSON output for production
		output = os.Stdout
	}

	// Create logger
	logger := zerolog.New(output).
		With().
		Timestamp().
		Logger()

	// Set as global logger
	log.Logger = logger

	return logger
}

// WithCorrelationID adds a correlation ID to the context
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, CorrelationIDKey, correlationID)
}

// GetCorrelationID retrieves the correlation ID from context
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(CorrelationIDKey).(string); ok {
		return id
	}
	return ""
}

// WithUserID adds a user ID to the context
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, UserIDKey, userID)
}

// GetUserID retrieves the user ID from context
func GetUserID(ctx context.Context) string {
	if id, ok := ctx.Value(UserIDKey).(string); ok {
		return id
	}
	return ""
}

// LoggerFromContext returns a logger with context values
func LoggerFromContext(ctx context.Context, baseLogger zerolog.Logger) zerolog.Logger {
	logger := baseLogger

	if correlationID := GetCorrelationID(ctx); correlationID != "" {
		logger = logger.With().Str("correlation_id", correlationID).Logger()
	}

	if userID := GetUserID(ctx); userID != "" {
		logger = logger.With().Str("user_id", userID).Logger()
	}

	return logger
}
