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
	// ModeOBO is the mode for a client that holds a token for this API rather
	// than for Microsoft Graph.
	//
	// The caller presents an access token whose audience is this application.
	// Its signature is verified against the tenant's published keys, and it is
	// then exchanged through the OAuth 2.0 On-Behalf-Of flow for a delegated
	// Microsoft Graph token.
	//
	// This is the only mode in which the service holds a credential of its
	// own, and therefore the only one in which it has standing privilege: the
	// client secret can mint a delegated Graph token for any user whose
	// assertion it has seen. See OBOExchanger.
	ModeOBO ValidationMode = iota

	// ModeVerifiedIdentity is the strongest mode without standing privilege. The caller presents two
	// credentials: a Microsoft Graph access token in the Authorization header,
	// which is forwarded to Graph unchanged, and an Entra identity assertion in
	// a separate header, whose signature is verified against the tenant's
	// published keys.
	//
	// The assertion exists because the Graph token cannot be verified by
	// anyone but Graph. Verifying a credential that can be verified gives the
	// audit trail and the rate limiter an identity backed by Entra's signature
	// instead of by an attacker-controlled claim.
	ModeVerifiedIdentity

	// ModeGraphPassthrough forwards the Graph token after inspecting its
	// shape, audience, tenant and expiry. No signature is verified, because
	// none can be. The identity recorded is the one the token claims.
	ModeGraphPassthrough

	// ModeDisabled accepts any bearer token with no inspection at all.
	// Development only; the configuration loader refuses it elsewhere.
	ModeDisabled
)

// ParseValidationMode maps a configuration value onto a mode.
func ParseValidationMode(s string) (ValidationMode, error) {
	switch s {
	case "obo":
		return ModeOBO, nil
	case "verified_identity":
		return ModeVerifiedIdentity, nil
	case "graph_passthrough":
		return ModeGraphPassthrough, nil
	case "disabled":
		return ModeDisabled, nil
	default:
		return 0, fmt.Errorf(
			"auth mode %q is not recognised; use obo, verified_identity, graph_passthrough or disabled", s)
	}
}

// String renders the mode for startup logging.
func (m ValidationMode) String() string {
	switch m {
	case ModeOBO:
		return "obo"
	case ModeVerifiedIdentity:
		return "verified_identity"
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
	// Inspector reads the forwarded Graph token's claims. Required unless Mode
	// is ModeDisabled.
	Inspector *GraphTokenInspector

	// Verifier checks the identity assertion. Required in
	// ModeVerifiedIdentity.
	Verifier *IdentityVerifier

	// AssertionHeader is the header carrying the identity assertion. The
	// assertion travels in its own header because the Authorization header is
	// already carrying the Graph credential, and the two are different kinds
	// of thing: one is forwarded, the other is verified and discarded.
	AssertionHeader string

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
	verifier            *IdentityVerifier
	assertionHeader     string
	mode                ValidationMode
	resourceMetadataURL string
	logger              *zerolog.Logger
	metrics             *observability.Metrics
}

// DefaultAssertionHeader carries the Entra identity assertion.
const DefaultAssertionHeader = "X-Identity-Assertion"

// NewMiddleware builds the authentication middleware.
func NewMiddleware(cfg MiddlewareConfig) (*Middleware, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("auth middleware: logger is required")
	}
	// The Graph inspector reads a forwarded Graph token. In obo mode the caller
	// presents a token for this API instead, so there is nothing for it to
	// inspect.
	needsInspector := cfg.Mode == ModeGraphPassthrough || cfg.Mode == ModeVerifiedIdentity
	if needsInspector && cfg.Inspector == nil {
		return nil, fmt.Errorf("auth middleware: inspector is required in %s mode", cfg.Mode)
	}
	if (cfg.Mode == ModeVerifiedIdentity || cfg.Mode == ModeOBO) && cfg.Verifier == nil {
		return nil, fmt.Errorf("auth middleware: identity verifier is required in %s mode", cfg.Mode)
	}

	assertionHeader := cfg.AssertionHeader
	if assertionHeader == "" {
		assertionHeader = DefaultAssertionHeader
	}

	return &Middleware{
		inspector:           cfg.Inspector,
		verifier:            cfg.Verifier,
		assertionHeader:     assertionHeader,
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

		ctx, claims, err := m.authenticate(r.Context(), token, r.Header.Get(m.assertionHeader))
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
			Bool("identity_verified", m.mode == ModeVerifiedIdentity).
			Msg("Request authenticated")

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate inspects the token and returns a context carrying the caller's
// claimed identity and the credential itself.
func (m *Middleware) authenticate(ctx context.Context, graphToken, assertion string) (context.Context, *Claims, error) {
	correlationID := uuid.NewString()

	withContext := func(claims *Claims) (context.Context, *Claims, error) {
		out := observability.WithUserID(ctx, claims.GetUserID())
		out = observability.WithCorrelationID(out, correlationID)
		return ContextWithToken(out, graphToken), claims, nil
	}

	if m.mode == ModeDisabled {
		return withContext(&Claims{ObjectID: "auth-disabled"})
	}

	// In obo mode the Authorization header carries a token for this API, not
	// for Graph. It is verified here; the exchange happens lazily when a tool
	// actually needs Graph, so initialize and tools/list cost no token round
	// trip.
	if m.mode == ModeOBO {
		verified, err := m.verifier.Verify(ctx, graphToken)
		if err != nil {
			return nil, nil, fmt.Errorf("access token rejected: %w", err)
		}
		return withContext(verified)
	}

	// The Graph token is inspected in every mode. It is the credential that
	// will actually be used, so a malformed or foreign-tenant one is refused
	// before any Graph call regardless of what the assertion says.
	graphClaims, err := m.inspector.Inspect(graphToken)
	if err != nil {
		return nil, nil, err
	}

	if m.mode == ModeGraphPassthrough {
		return withContext(graphClaims)
	}

	if assertion == "" {
		return nil, nil, fmt.Errorf(
			"missing identity assertion in %s; configure the client to send a verifiable Entra "+
				"token there (in LibreChat: {{LIBRECHAT_OPENID_ID_TOKEN}})", m.assertionHeader)
	}

	verified, err := m.verifier.Verify(ctx, assertion)
	if err != nil {
		return nil, nil, fmt.Errorf("identity assertion rejected: %w", err)
	}

	if err := requireSameSubject(verified, graphClaims); err != nil {
		return nil, nil, err
	}

	// The verified claims are the ones recorded and rate limited on. The Graph
	// token's own claims are never used for that in this mode.
	return withContext(verified)
}

// requireSameSubject rejects a request whose two credentials describe different
// people.
//
// The binding is not cryptographic: nothing in the assertion commits to the
// Graph token, so a caller can pair a valid assertion with a Graph token for
// another user. What that buys an attacker is nothing, because they must
// already hold the other user's Graph token, and holding it means they can call
// Microsoft Graph directly without this service. What the check does buy is
// audit integrity: a Graph call cannot be recorded under an identity that does
// not match the credential used to make it.
func requireSameSubject(verified, graph *Claims) error {
	if normalizeTenant(verified.TenantID) != normalizeTenant(graph.TenantID) {
		return fmt.Errorf("identity assertion and Graph token are from different directories")
	}

	// Compared only when the Graph token actually carries an object ID. A
	// missing claim is not treated as a match.
	if verified.ObjectID != "" && graph.ObjectID != "" &&
		!strings.EqualFold(verified.ObjectID, graph.ObjectID) {
		return fmt.Errorf("identity assertion and Graph token name different users")
	}
	return nil
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

	description := "A Microsoft Graph access token is required in the Authorization header."
	if m.mode == ModeOBO {
		description = "An access token issued for this API is required in the Authorization header."
	}
	if m.mode == ModeVerifiedIdentity {
		description += fmt.Sprintf(" An Entra identity assertion is required in %s.", m.assertionHeader)
	}

	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             errorCode,
		"error_description": description,
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
