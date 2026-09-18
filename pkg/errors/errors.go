package errors

import (
	"errors"
	"fmt"
)

// ErrorCode represents JSON-RPC 2.0 error codes
type ErrorCode int

const (
	// JSON-RPC 2.0 standard error codes
	ErrCodeParseError     ErrorCode = -32700
	ErrCodeInvalidRequest ErrorCode = -32600
	ErrCodeMethodNotFound ErrorCode = -32601
	ErrCodeInvalidParams  ErrorCode = -32602
	ErrCodeInternalError  ErrorCode = -32603

	// Custom application error codes
	ErrCodeTokenValidation ErrorCode = -32001
	ErrCodeGraphAPI        ErrorCode = -32002
	ErrCodeRateLimit       ErrorCode = -32004
	ErrCodeCircuitOpen     ErrorCode = -32005
	ErrCodeClientError     ErrorCode = -32006 // 4xx client errors (not retryable, not infrastructure)
)

// AppError represents an application error with context
type AppError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Details map[string]interface{}
}

// Error implements the error interface
func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap implements error unwrapping
func (e *AppError) Unwrap() error {
	return e.Cause
}

// NewAppError creates a new application error
func NewAppError(code ErrorCode, message string, cause error, details map[string]interface{}) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Cause:   cause,
		Details: details,
	}
}

// NewTokenValidationError creates a token validation error
func NewTokenValidationError(cause error) *AppError {
	return &AppError{
		Code:    ErrCodeTokenValidation,
		Message: "Token validation failed",
		Cause:   cause,
	}
}

// NewGraphAPIError creates a Graph API error
func NewGraphAPIError(cause error, details map[string]interface{}) *AppError {
	return &AppError{
		Code:    ErrCodeGraphAPI,
		Message: "Microsoft Graph API error",
		Cause:   cause,
		Details: details,
	}
}

// NewRateLimitError creates a rate limit error
func NewRateLimitError(message string) *AppError {
	return &AppError{
		Code:    ErrCodeRateLimit,
		Message: message,
	}
}

// NewCircuitOpenError creates a circuit breaker open error
func NewCircuitOpenError(service string) *AppError {
	return &AppError{
		Code:    ErrCodeCircuitOpen,
		Message: fmt.Sprintf("Circuit breaker open for %s", service),
	}
}

// NewClientError creates a client-side error (4xx) that should not trip the circuit breaker
func NewClientError(statusCode int, cause error, details map[string]interface{}) *AppError {
	return &AppError{
		Code:    ErrCodeClientError,
		Message: fmt.Sprintf("Client error (HTTP %d)", statusCode),
		Cause:   cause,
		Details: details,
	}
}

// Wrap wraps an error with additional context
func Wrap(err error, message string) error {
	return fmt.Errorf("%s: %w", message, err)
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == ErrCodeRateLimit || appErr.Code == ErrCodeGraphAPI
	}
	return false
}

// IsCircuitBreakerFailure returns true for errors that should count as infrastructure
// failures for the circuit breaker. Client-side errors (auth, bad params) should not
// trip the breaker since they are not transient infrastructure issues.
func IsCircuitBreakerFailure(err error) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		switch appErr.Code {
		case ErrCodeGraphAPI, ErrCodeRateLimit, ErrCodeInternalError:
			return true
		default:
			// Token validation, circuit open, client errors etc. are not infrastructure failures
			return false
		}
	}
	// Unknown errors default to NOT counting as failures.
	// Only properly classified infrastructure errors (GraphAPI, RateLimit, InternalError) trip the breaker.
	return false
}

// GetErrorCode extracts the error code from an error
func GetErrorCode(err error) ErrorCode {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return ErrCodeInternalError
}
