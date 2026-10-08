package config

import (
	"testing"
	"time"
)

func TestLoadWithoutOptionalProviderConfiguration(t *testing.T) {
	t.Setenv("AZURE_TENANT_ID", "tenant-id")
	t.Setenv("GRAPH_TIMEOUT", "45s")
	// PUBLIC_URL defaults to a loopback address, which Validate refuses
	// outside a development environment.
	t.Setenv("ENVIRONMENT", "development")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned an error without optional provider configuration: %v", err)
	}

	if cfg.AzureTenantID != "tenant-id" {
		t.Fatalf("Azure configuration was not loaded correctly: %+v", cfg)
	}
	if cfg.GraphTimeout != 45*time.Second {
		t.Fatalf("expected Graph timeout 45s, got %s", cfg.GraphTimeout)
	}
}

// baseEnv sets the variables every configuration needs, leaving the subject of
// each test to the test itself.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AZURE_TENANT_ID", "tenant-id")
	t.Setenv("PUBLIC_URL", "https://msgraph-mcp.example.com")
}

func TestAuthBypassesAreRefusedOutsideDevelopment(t *testing.T) {
	bypasses := []string{"DISABLE_AUTH"}
	deployed := []string{"production", "prod", "staging"}

	for _, bypass := range bypasses {
		for _, environment := range deployed {
			t.Run(bypass+"/"+environment, func(t *testing.T) {
				baseEnv(t)
				t.Setenv("ENVIRONMENT", environment)
				t.Setenv(bypass, "true")

				if _, err := Load(); err == nil {
					t.Fatalf("%s=true was accepted with ENVIRONMENT=%s", bypass, environment)
				}
			})
		}
	}
}

func TestAuthBypassesAreAllowedInDevelopment(t *testing.T) {
	for _, environment := range []string{"development", "dev"} {
		t.Run(environment, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("ENVIRONMENT", environment)
			t.Setenv("DISABLE_AUTH", "true")

			if _, err := Load(); err != nil {
				t.Fatalf("development configuration was refused: %v", err)
			}
		})
	}
}

// An unrecognised ENVIRONMENT must not quietly behave as neither production
// nor development. An empty value is not tested here: the env parser treats it
// as unset and applies the "production" default, which is the intended
// fail-closed outcome.
func TestUnknownEnvironmentIsRefused(t *testing.T) {
	for _, environment := range []string{"Production", "PROD", "prod ", "preprod", "test"} {
		t.Run(environment, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("ENVIRONMENT", environment)

			if _, err := Load(); err == nil {
				t.Fatalf("ENVIRONMENT=%q was accepted", environment)
			}
		})
	}
}

func TestPublicURLValidation(t *testing.T) {
	tests := []struct {
		name        string
		environment string
		publicURL   string
		wantErr     bool
	}{
		{"https host in production", "production", "https://mcp.example.com", false},
		{"https host with path", "production", "https://mcp.example.com/gateway", false},
		{"plaintext in production", "production", "http://mcp.example.com", true},
		{"loopback in production", "production", "https://localhost:8080", true},
		{"loopback ip in production", "production", "https://127.0.0.1:8080", true},
		{"unspecified ip in production", "production", "https://0.0.0.0:8080", true},
		{"relative url", "production", "/mcp", true},
		{"empty", "production", "", true},
		{"loopback in development", "development", "http://localhost:8080", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("ENVIRONMENT", tt.environment)
			t.Setenv("PUBLIC_URL", tt.publicURL)

			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("PUBLIC_URL=%q: error = %v, wantErr = %v", tt.publicURL, err, tt.wantErr)
			}
		})
	}
}
