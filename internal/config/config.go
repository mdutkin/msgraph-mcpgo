package config

import (
	"fmt"
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
	RateLimitPerUser int `env:"RATE_LIMIT_PER_USER" envDefault:"100"`

	// Testing
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
	return cfg, nil
}

// IsDevelopment returns true if running in development mode
func (c *Config) IsDevelopment() bool {
	return c.Environment == "development" || c.Environment == "dev"
}

// IsProduction returns true if running in production mode
func (c *Config) IsProduction() bool {
	return c.Environment == "production" || c.Environment == "prod"
}
