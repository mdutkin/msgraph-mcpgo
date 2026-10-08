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

	// Azure AD / EntraID
	AzureTenantID     string `env:"AZURE_TENANT_ID,required"`
	AzureClientID     string `env:"AZURE_CLIENT_ID,required"`
	AzureClientSecret string `env:"AZURE_CLIENT_SECRET,required"`

	// Timeouts
	GraphTimeout time.Duration `env:"GRAPH_TIMEOUT" envDefault:"60s"`

	// Caching
	TokenCacheTTL time.Duration `env:"TOKEN_CACHE_TTL" envDefault:"5m"`
	JWKSCacheTTL  time.Duration `env:"JWKS_CACHE_TTL" envDefault:"24h"`

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

	// Testing
	//
	// DisableAuth and SkipTokenValidation are rejected outside a development
	// environment by Validate. See that method for why.
	DisableAuth bool `env:"DISABLE_AUTH" envDefault:"false"`

	// SkipTokenValidation skips local JWT signature verification; token is still
	// passed to MS Graph OBO, which validates it. Use when JWKS verification
	// fails in some environments (e.g. PRD) but OBO succeeds.
	SkipTokenValidation bool `env:"SKIP_TOKEN_VALIDATION" envDefault:"false"`
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

	if !knownEnvironments[c.Environment] {
		problems = append(problems, fmt.Sprintf(
			"ENVIRONMENT=%q is not recognised; use one of development, dev, staging, production, prod",
			c.Environment))
	}

	if !c.IsDevelopment() {
		if c.DisableAuth {
			problems = append(problems, fmt.Sprintf(
				"DISABLE_AUTH=true is refused when ENVIRONMENT=%q: it accepts any bearer token "+
					"and forwards it to Microsoft Graph without validation", c.Environment))
		}
		if c.SkipTokenValidation {
			problems = append(problems, fmt.Sprintf(
				"SKIP_TOKEN_VALIDATION=true is refused when ENVIRONMENT=%q: token signatures, "+
					"audience and issuer are not verified locally, and the identity written to "+
					"the audit log is taken from an unverified token", c.Environment))
		}
	}

	problems = append(problems, c.validatePublicURL()...)

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
	// identifier and in every 401 challenge, so a default or internal value
	// gives clients a metadata URL they cannot reach.
	if u.Scheme != "https" {
		problems = append(problems, fmt.Sprintf(
			"PUBLIC_URL=%q must use https when ENVIRONMENT=%q: the value is advertised to "+
				"clients as the OAuth protected resource identifier", c.PublicURL, c.Environment))
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
