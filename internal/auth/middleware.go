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

// ValidationMode selects how an incoming credential is treated.
type ValidationMode int

const (
	// ModeGraphPassthrough is the production mode. The caller presents a
	// Microsoft Graph access token, which is inspected for shape, audience,
	// tenant and expiry, and then forwarded to Graph unchanged.
	ModeGraphPassthrough ValidationMode = iota

	// ModeDisabled accepts any bearer token with no inspection at all.
	// Development only; the configuration loader refuses it elsewhere.
	ModeDisabled
)

// String renders the mode for startup logging.
func (m ValidationMode) String() string {
	switch m {
	case ModeGraphPassthrough:
		return "graph_passthrough"
	case ModeDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// MiddlewareConfig configures the HTTP authentication middleware.
type MiddlewareConfig struct {
	// Inspector reads the forwarded token's claims. Required unless Mode is
	// ModeDisabled.
	Inspector *GraphTokenInspector

	// Mode selects how the credential is treated.
	Mode ValidationMode

	// ResourceMetadataURL is the absolute URL of this server's OAuth 2.0
	// Protected Resource Metadata document (RFC 9728), advertised in the
	// WWW-Authenticate header of every 401 so a compliant MCP client can
	// discover where to obtain a token. May be empty to omit the hint.
	ResourceMetadataURL string

	Logger  *zerolog.Logger
	Metrics *observability.Metrics
}

// Middleware authenticates every request before it reaches the MCP transport.
//
// There is no unauthenticated path. An earlier revision exempted the MCP
// discovery methods so a client could enumerate the server before presenting a
// credential, which published the full tool inventory, every description and
// every input schema to any caller that could reach the load balancer.
// Discovery is authenticated like everything else; clients learn where to get a
// token from the RFC 9728 metadata document instead.
//
// This middleware establishes who the caller claims to be, which is what the
// audit trail and the rate limiter key on. It does not establish what the
// caller may do. That is decided by the delegated permissions inside the
// forwarded token and enforced by Microsoft Graph. See GraphTokenInspector.
type Middleware struct {
	inspector           *GraphTokenInspector
	mode                ValidationMode
	resourceMetadataURL string
	logger              *zerolog.Logger
	metrics             *observability.Metrics
}

// NewMiddleware builds the authentication middleware.
func NewMiddleware(cfg MiddlewareConfig) (*Middleware, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("auth middleware: logger is required")
	}
	if cfg.Mode != ModeDisabled && cfg.Inspector == nil {
		return nil, fmt.Errorf("auth middleware: inspector is required in %s mode", cfg.Mode)
	}
	return &Middleware{
		inspector:           cfg.Inspector,
		mode:                cfg.Mode,
		resourceMetadataURL: cfg.ResourceMetadataURL,
		logger:              cfg.Logger,
		metrics:             cfg.Metrics,
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

		ctx, claims, err := m.authenticate(r.Context(), token)
		if err != nil {
			m.reject(w, r, "invalid_token", err)
			return
		}

		m.count("accepted")

		logger := observability.LoggerFromContext(ctx, *m.logger)
		logger.Debug().
			Str("user_email", claims.Email).
			Str("tenant_id", claims.TenantID).
			Str("validation_mode", m.mode.String()).
			Msg("Request authenticated")

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate inspects the token and returns a context carrying the caller's
// claimed identity and the credential itself.
func (m *Middleware) authenticate(ctx context.Context, token string) (context.Context, *Claims, error) {
	correlationID := uuid.NewString()

	if m.mode == ModeDisabled {
		claims := &Claims{ObjectID: "auth-disabled"}
		ctx = observability.WithUserID(ctx, claims.GetUserID())
		ctx = observability.WithCorrelationID(ctx, correlationID)
		return ContextWithToken(ctx, token), claims, nil
	}

	claims, err := m.inspector.Inspect(token)
	if err != nil {
		return nil, nil, err
	}

	ctx = observability.WithUserID(ctx, claims.GetUserID())
	ctx = observability.WithCorrelationID(ctx, correlationID)
	return ContextWithToken(ctx, token), claims, nil
}

// reject writes a 401 carrying the discovery hint required by the MCP
// authorization specification and RFC 9728 section 5.1.
//
// The cause is logged but not returned. A rejection message can name the
// expected tenant and audience, which only helps someone probing the
// deployment.
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
		"error_description": "A Microsoft Graph access token is required in the Authorization header.",
	})

	m.count("rejected")
	m.logger.Warn().
		Err(cause).
		Str("error_code", errorCode).
		Str("path", r.URL.Path).
		Str("remote_addr", r.RemoteAddr).
		Msg("Rejected MCP request")
}

func (m *Middleware) count(outcome string) {
	if m.metrics != nil {
		m.metrics.TokenValidationTotal.WithLabelValues(outcome).Inc()
	}
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
