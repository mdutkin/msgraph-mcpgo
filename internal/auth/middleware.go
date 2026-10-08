package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// ValidationMode selects how strictly an incoming bearer token is checked.
type ValidationMode int

const (
	// ModeVerify is the only mode fit for production. The token signature is
	// verified against the tenant JWKS, and the audience, issuer, expiry and
	// subject claims are all enforced locally before any downstream call.
	ModeVerify ValidationMode = iota

	// ModeSkipSignature parses claims without verifying the signature and
	// relies on Microsoft Entra rejecting a forged assertion during the OBO
	// exchange. It exists for environments where JWKS retrieval is blocked.
	// It removes local defence in depth: an unverified token reaches the OBO
	// call, and the identity recorded in the audit log is attacker-controlled.
	ModeSkipSignature

	// ModeDisabled performs no validation at all and passes the token
	// straight through. Development only.
	ModeDisabled
)

// String renders the mode for startup logging.
func (m ValidationMode) String() string {
	switch m {
	case ModeVerify:
		return "verify"
	case ModeSkipSignature:
		return "skip_signature"
	case ModeDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// MiddlewareConfig configures the HTTP authentication middleware.
type MiddlewareConfig struct {
	// Validator verifies tokens. Required unless Mode is ModeDisabled.
	Validator *TokenValidator

	// Mode selects the validation strictness.
	Mode ValidationMode

	// ResourceMetadataURL is the absolute URL of this server's OAuth 2.0
	// Protected Resource Metadata document (RFC 9728). It is advertised in the
	// WWW-Authenticate header of every 401 so that a compliant MCP client can
	// discover which authorization server to use. May be empty, in which case
	// the hint is omitted.
	ResourceMetadataURL string

	Logger *zerolog.Logger
}

// Middleware authenticates every request before it reaches the MCP transport.
//
// There is no unauthenticated path. An earlier revision exempted the MCP
// discovery methods (initialize, tools/list, resources/list, prompts/list) so
// that a client could enumerate the server before presenting a credential.
// That published the full tool inventory, every tool description and every
// input schema to any anonymous caller that could reach the load balancer,
// which is a map of the Microsoft 365 surface this gateway exposes. Discovery
// is now authenticated like everything else; clients learn where to get a
// token from the RFC 9728 metadata document instead, which is the mechanism
// the MCP authorization specification defines for the purpose.
type Middleware struct {
	validator           *TokenValidator
	mode                ValidationMode
	resourceMetadataURL string
	logger              *zerolog.Logger
}

// NewMiddleware builds the authentication middleware.
func NewMiddleware(cfg MiddlewareConfig) (*Middleware, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("auth middleware: logger is required")
	}
	if cfg.Mode != ModeDisabled && cfg.Validator == nil {
		return nil, fmt.Errorf("auth middleware: validator is required in %s mode", cfg.Mode)
	}
	return &Middleware{
		validator:           cfg.Validator,
		mode:                cfg.Mode,
		resourceMetadataURL: cfg.ResourceMetadataURL,
		logger:              cfg.Logger,
	}, nil
}

// Handler wraps next with bearer token authentication.
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := bearerToken(r)
		if err != nil {
			m.reject(w, r, "invalid_request", err)
			return
		}

		ctx, userID, userEmail, err := m.authenticate(r.Context(), token)
		if err != nil {
			m.reject(w, r, "invalid_token", err)
			return
		}

		logger := observability.LoggerFromContext(ctx, *m.logger)
		logger.Debug().
			Str("user_id", userID).
			Str("user_email", userEmail).
			Str("validation_mode", m.mode.String()).
			Msg("Request authenticated")

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate validates the token and returns a context carrying the caller's
// identity and credential.
func (m *Middleware) authenticate(ctx context.Context, token string) (context.Context, string, string, error) {
	correlationID := uuid.NewString()

	switch m.mode {
	case ModeDisabled:
		ctx = observability.WithUserID(ctx, "auth-disabled")
		ctx = observability.WithCorrelationID(ctx, correlationID)
		return ContextWithToken(ctx, token), "auth-disabled", "", nil

	case ModeSkipSignature:
		// Claims are read for the audit trail only. They are not trusted:
		// Microsoft Entra rejects a forged assertion during the OBO exchange,
		// so a forged token yields no Graph access, but it would still be
		// recorded under the identity it claims.
		claims, err := m.validator.ParseClaimsWithoutVerification(token)
		if err != nil {
			return nil, "", "", fmt.Errorf("token claims are unreadable: %w", err)
		}
		ctx = observability.WithUserID(ctx, claims.GetUserID())
		ctx = observability.WithCorrelationID(ctx, correlationID)
		return ContextWithToken(ctx, token), claims.GetUserID(), claims.Email, nil

	default:
		claims, err := m.validator.ValidateToken(ctx, token)
		if err != nil {
			return nil, "", "", err
		}
		ctx = observability.WithUserID(ctx, claims.GetUserID())
		ctx = observability.WithCorrelationID(ctx, correlationID)
		return ContextWithToken(ctx, token), claims.GetUserID(), claims.Email, nil
	}
}

// reject writes a 401 carrying the discovery hint required by the MCP
// authorization specification and RFC 9728 section 5.1.
//
// The error reason is returned to the caller but the underlying error is not:
// a validation failure message can disclose expected audience and issuer
// values, which only helps someone probing the deployment. The detail goes to
// the log instead.
func (m *Middleware) reject(w http.ResponseWriter, r *http.Request, errorCode string, cause error) {
	challenge := fmt.Sprintf("Bearer error=%q", errorCode)
	if m.resourceMetadataURL != "" {
		challenge += fmt.Sprintf(", resource_metadata=%q", m.resourceMetadataURL)
	}

	w.Header().Set("WWW-Authenticate", challenge)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)

	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errorCode,
		"error_description": "A valid Microsoft Entra ID bearer token is required.",
	})

	m.logger.Warn().
		Err(cause).
		Str("error_code", errorCode).
		Str("path", r.URL.Path).
		Str("remote_addr", r.RemoteAddr).
		Msg("Rejected unauthenticated MCP request")
}

// bearerToken extracts the credential from the Authorization header.
func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", fmt.Errorf("missing Authorization header")
	}

	scheme, credential, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") {
		return "", fmt.Errorf("Authorization header is not a bearer credential")
	}

	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", fmt.Errorf("bearer credential is empty")
	}
	return credential, nil
}
