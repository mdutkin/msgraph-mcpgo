package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v10"
	"github.com/joho/godotenv"
)

// Config holds all application configuration
type Config struct {
	// Server
	Environment string `env:"ENVIRONMENT" envDefault:"production"`
	ServerPort  int    `env:"SERVER_PORT" envDefault:"8080"`

	// PublicURL is the externally reachable base URL of this service, for
	// example https://msgraph-mcp.example.com. It is the OAuth 2.0 protected
	// resource identifier published in the RFC 9728 metadata document and
	// advertised in the WWW-Authenticate header of every 401, so it must match
	// the hostname clients actually connect to, not the container address.
	PublicURL string `env:"PUBLIC_URL" envDefault:"http://localhost:8080"`

	// NetworkExposure declares where this instance is reachable from, which
	// decides how strictly PublicURL is checked.
	//
	// "internet" is the default and the strict setting: PublicURL must be
	// https, because the value is handed to clients and a plaintext one
	// invites a credential to cross the public network.
	//
	// "internal" relaxes that for a deployment reached only from inside the
	// private network, such as an ECS service addressed over Service Connect
	// at http://msgraph-mcp:8080, where TLS terminates at the perimeter and
	// there is no https name to publish.
	NetworkExposure string `env:"NETWORK_EXPOSURE" envDefault:"internet"`

	// ToolPolicyFile points at the YAML document that decides which tools and
	// resources this deployment exposes. When the variable is set, a missing
	// file is a startup error. When it is unset, a missing file at the default
	// path exposes everything, which preserves the behaviour of a deployment
	// that has no policy.
	ToolPolicyFile string `env:"TOOL_POLICY_FILE"`

	// MCPStateless keeps the Streamable HTTP transport free of per-session
	// state. Required whenever more than one task serves the same load
	// balancer target group, because session state lives in task memory.
	MCPStateless bool `env:"MCP_STATELESS" envDefault:"true"`

	// Microsoft Entra ID
	//
	// The tenant is always required: it bounds which directory's tokens are
	// accepted and names the authorization server in the published resource
	// metadata. The client ID and secret are needed only for AUTH_MODE=obo.
	AzureTenantID string `env:"AZURE_TENANT_ID,required"`

	// AzureClientID is this service's own application. Required when
	// AUTH_MODE=obo: it is the audience the caller's token must name, and the
	// client this service authenticates as during the exchange.
	AzureClientID string `env:"AZURE_CLIENT_ID"`

	// AzureClientSecret authenticates this service to the token endpoint.
	// Required when AUTH_MODE=obo and never logged.
	//
	// Holding it is what gives this service standing privilege: it can mint a
	// delegated Graph token for any user whose assertion the service has seen.
	// Supply it from a secret store, never from an image or a task definition
	// literal.
	AzureClientSecret string `env:"AZURE_CLIENT_SECRET"`

	// GraphScopes are the delegated Microsoft Graph permissions requested
	// during the exchange, comma separated. Empty selects the full set the
	// tools need.
	//
	// Narrowing this is the lever that makes a withheld tool also a withheld
	// permission: excluding search_emails in the tool policy stops the model
	// calling it, while removing Mail.Read here stops the token being able to.
	GraphScopes []string `env:"GRAPH_SCOPES" envSeparator:","`

	// OBOExpiryMargin is how long before true expiry a cached delegated token
	// stops being served.
	OBOExpiryMargin time.Duration `env:"OBO_EXPIRY_MARGIN" envDefault:"5m"`

	// MaxRequestBytes bounds an MCP request body. The transport reads a body
	// in full, so without a bound one request can allocate as much memory as
	// the sender transmits. The default admits an upload_file call carrying a
	// file of roughly 24 MiB after base64 expansion.
	MaxRequestBytes int64 `env:"MAX_REQUEST_BYTES" envDefault:"33554432"`

	// Timeouts
	GraphTimeout time.Duration `env:"GRAPH_TIMEOUT" envDefault:"60s"`

	// ReadHeaderTimeout bounds how long a client may take to send request
	// headers. It is the defence against a slow-header connection holding a
	// goroutine and a load balancer slot open indefinitely.
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT" envDefault:"10s"`

	// ReadTimeout bounds the whole request read, WriteTimeout the response
	// write. WriteTimeout must exceed GraphTimeout, otherwise a slow but
	// successful Microsoft Graph call is cut off after the work is done.
	ReadTimeout  time.Duration `env:"READ_TIMEOUT" envDefault:"30s"`
	WriteTimeout time.Duration `env:"WRITE_TIMEOUT" envDefault:"120s"`
	IdleTimeout  time.Duration `env:"IDLE_TIMEOUT" envDefault:"120s"`

	// Observability
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
	MetricsPort int    `env:"METRICS_PORT" envDefault:"9090"`

	// Rate Limiting
	//
	// RateLimitPerUser is the sustained request allowance for one user, per
	// minute, per task. Zero disables throttling. The limit is not shared
	// between tasks; see internal/ratelimit for why.
	RateLimitPerUser int `env:"RATE_LIMIT_PER_USER" envDefault:"100"`

	// RateLimitBurst is how many requests a user may issue back to back
	// before the sustained rate applies. One MCP prompt often produces
	// several tool calls, so too small a burst throttles normal use. Zero
	// selects a quarter of RateLimitPerUser, with a floor of five.
	RateLimitBurst int `env:"RATE_LIMIT_BURST" envDefault:"0"`

	// RateLimitIdleTTL is how long an unused per-user bucket is retained.
	RateLimitIdleTTL time.Duration `env:"RATE_LIMIT_IDLE_TTL" envDefault:"15m"`

	// Authentication
	//
	// AuthMode selects how a caller is authenticated:
	//
	//   obo                The caller sends an access token issued for this API.
	//                      Its signature is verified against the tenant's
	//                      published keys, then it is exchanged through the
	//                      On-Behalf-Of flow for a delegated Graph token.
	//                      Requires AzureClientID and AzureClientSecret, and is
	//                      the only mode in which this service holds a
	//                      credential of its own.
	//
	//   verified_identity  The caller sends a Graph token in Authorization and
	//                      an Entra identity assertion in IdentityAssertionHeader.
	//                      The assertion's signature is verified against the
	//                      tenant's published keys, so the identity in the audit
	//                      log is backed by Entra rather than by a claim the
	//                      caller controls. Requires IdentityAudiences.
	//
	//   graph_passthrough  The caller sends only a Graph token. It is inspected
	//                      but no signature is verified, because Microsoft does
	//                      not publish signing keys for tokens issued to its own
	//                      APIs. The recorded identity is the one the token
	//                      claims.
	//
	//   disabled           No inspection at all. Development only; Validate
	//                      refuses it elsewhere.
	AuthMode string `env:"AUTH_MODE" envDefault:"graph_passthrough"`

	// IdentityAssertionHeader carries the verifiable Entra token in
	// verified_identity mode.
	IdentityAssertionHeader string `env:"IDENTITY_ASSERTION_HEADER" envDefault:"X-Identity-Assertion"`

	// IdentityAudiences are the accepted "aud" values of the assertion, one per
	// application allowed to vouch for a caller. For LibreChat's OpenID ID
	// token this is the LibreChat client ID.
	IdentityAudiences []string `env:"IDENTITY_AUDIENCES" envSeparator:","`

	// EntraTokenURL overrides the token endpoint used for the on-behalf-of
	// exchange. Leave empty for the public cloud; a sovereign cloud needs its
	// own authority.
	EntraTokenURL string `env:"ENTRA_TOKEN_URL"`

	// EntraDiscoveryURL overrides the OpenID configuration URL the signing keys
	// are discovered through. Leave empty for the public cloud. A sovereign
	// cloud needs its own authority, for example
	// https://login.microsoftonline.us/<tenant>/v2.0/.well-known/openid-configuration
	EntraDiscoveryURL string `env:"ENTRA_DISCOVERY_URL"`

	// JWKSRefreshInterval is how often Entra's signing keys are re-read in the
	// background. Microsoft's guidance is at least daily.
	JWKSRefreshInterval time.Duration `env:"JWKS_REFRESH_INTERVAL" envDefault:"12h"`

	// JWKSMinRefreshInterval is the floor between on-demand key refreshes
	// triggered by an unrecognised key identifier. It stops a caller sending
	// random key identifiers from turning this service into a request
	// amplifier against Entra.
	JWKSMinRefreshInterval time.Duration `env:"JWKS_MIN_REFRESH_INTERVAL" envDefault:"5m"`
}

// Load loads configuration from environment variables
func Load() (*Config, error) {
	// Load .env file if it exists (ignore error if file doesn't exist)
	_ = godotenv.Load()

	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// knownEnvironments are the accepted ENVIRONMENT values. An unrecognised value
// is rejected rather than tolerated: a typo such as "Production" or "prod "
// would otherwise satisfy neither IsProduction nor IsDevelopment, leaving the
// operator with a deployment whose posture is not what the variable says.
var knownNetworkExposures = map[string]bool{
	"internet": true,
	"internal": true,
}

var knownEnvironments = map[string]bool{
	"development": true,
	"dev":         true,
	"staging":     true,
	"production":  true,
	"prod":        true,
}

// Validate rejects a configuration that cannot be served safely.
//
// It fails closed. The two authentication bypasses are permitted only when
// ENVIRONMENT names a development environment, so any value that is not
// explicitly a development one, including an empty or misspelled value,
// refuses to start with a bypass enabled. Refusing at startup is the point:
// a bypass that is silently honoured in a deployed environment produces a
// service that authenticates nobody while reporting itself healthy, and
// nothing downstream can detect the difference.
func (c *Config) Validate() error {
	var problems []string

	if !knownNetworkExposures[c.NetworkExposure] {
		problems = append(problems, fmt.Sprintf(
			"NETWORK_EXPOSURE=%q is not recognised; use \"internet\" or \"internal\"", c.NetworkExposure))
	}

	if !knownEnvironments[c.Environment] {
		problems = append(problems, fmt.Sprintf(
			"ENVIRONMENT=%q is not recognised; use one of development, dev, staging, production, prod",
			c.Environment))
	}

	switch c.AuthMode {
	case "obo":
		if c.AzureClientID == "" {
			problems = append(problems, "AZURE_CLIENT_ID is required when AUTH_MODE=obo: it is the "+
				"audience the caller's token must name and the client this service authenticates as")
		}
		if c.AzureClientSecret == "" {
			problems = append(problems, "AZURE_CLIENT_SECRET is required when AUTH_MODE=obo: the "+
				"on-behalf-of grant authenticates this service as a confidential client")
		}
	case "verified_identity":
		if len(c.IdentityAudiences) == 0 {
			problems = append(problems, "IDENTITY_AUDIENCES is required when AUTH_MODE=verified_identity: "+
				"without it any assertion the tenant ever issued, for any application, would be "+
				"accepted as proof of identity here")
		}
		if c.IdentityAssertionHeader == "" {
			problems = append(problems, "IDENTITY_ASSERTION_HEADER must not be empty when AUTH_MODE=verified_identity")
		}
	case "graph_passthrough":
		// Nothing further to check.
	case "disabled":
		if !c.IsDevelopment() {
			problems = append(problems, fmt.Sprintf(
				"AUTH_MODE=disabled is refused when ENVIRONMENT=%q: it accepts any bearer token "+
					"with no inspection, so a request cannot be attributed to a user in the "+
					"audit log and the rate limiter cannot tell callers apart", c.Environment))
		}
	default:
		problems = append(problems, fmt.Sprintf(
			"AUTH_MODE=%q is not recognised; use verified_identity, graph_passthrough or disabled",
			c.AuthMode))
	}

	if c.JWKSRefreshInterval <= 0 {
		problems = append(problems, fmt.Sprintf("JWKS_REFRESH_INTERVAL=%s must be positive", c.JWKSRefreshInterval))
	}
	if c.JWKSMinRefreshInterval <= 0 {
		problems = append(problems, fmt.Sprintf("JWKS_MIN_REFRESH_INTERVAL=%s must be positive", c.JWKSMinRefreshInterval))
	}

	problems = append(problems, c.validatePublicURL()...)

	if c.MaxRequestBytes <= 0 {
		problems = append(problems, fmt.Sprintf(
			"MAX_REQUEST_BYTES=%d must be positive", c.MaxRequestBytes))
	}

	// A response write deadline shorter than the Graph operation timeout cuts
	// off a slow but successful call after the work has already been done and
	// the Graph side effect has already happened.
	if c.WriteTimeout <= c.GraphTimeout {
		problems = append(problems, fmt.Sprintf(
			"WRITE_TIMEOUT=%s must exceed GRAPH_TIMEOUT=%s, otherwise a slow Microsoft Graph call "+
				"is cut off after it has already taken effect", c.WriteTimeout, c.GraphTimeout))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// validatePublicURL checks the OAuth protected resource identifier.
func (c *Config) validatePublicURL() []string {
	var problems []string

	u, err := url.Parse(c.PublicURL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return []string{fmt.Sprintf(
			"PUBLIC_URL=%q must be an absolute URL such as https://msgraph-mcp.example.com", c.PublicURL)}
	}

	if c.IsDevelopment() {
		return nil
	}

	// Outside development the value is published to clients as the resource
	// identifier and in every 401 challenge, so a default or unreachable value
	// gives clients a metadata URL they cannot use.
	if u.Scheme != "https" && c.NetworkExposure != "internal" {
		problems = append(problems, fmt.Sprintf(
			"PUBLIC_URL=%q must use https when ENVIRONMENT=%q and NETWORK_EXPOSURE=%q: the value "+
				"is advertised to clients as the OAuth protected resource identifier. Set "+
				"NETWORK_EXPOSURE=internal if this instance is only reachable inside the "+
				"private network", c.PublicURL, c.Environment, c.NetworkExposure))
	}
	if isLoopbackOrUnspecified(u.Hostname()) {
		problems = append(problems, fmt.Sprintf(
			"PUBLIC_URL=%q points at the container itself when ENVIRONMENT=%q: set it to the "+
				"hostname clients connect to", c.PublicURL, c.Environment))
	}

	return problems
}

func isLoopbackOrUnspecified(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}

// IsDevelopment returns true if running in development mode
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development" || c.Environment == "dev"
}

// IsProduction returns true if running in production mode
func (c *Config) IsProduction() bool {
	return c.Environment == "production" || c.Environment == "prod"
}
