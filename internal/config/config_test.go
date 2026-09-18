package config

import (
	"testing"
	"time"
)

func TestLoadWithoutOptionalProviderConfiguration(t *testing.T) {
	t.Setenv("AZURE_TENANT_ID", "tenant-id")
	t.Setenv("AZURE_CLIENT_ID", "client-id")
	t.Setenv("AZURE_CLIENT_SECRET", "client-secret")
	t.Setenv("GRAPH_TIMEOUT", "45s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned an error without optional provider configuration: %v", err)
	}

	if cfg.AzureTenantID != "tenant-id" || cfg.AzureClientID != "client-id" || cfg.AzureClientSecret != "client-secret" {
		t.Fatalf("Azure configuration was not loaded correctly: %+v", cfg)
	}
	if cfg.GraphTimeout != 45*time.Second {
		t.Fatalf("expected Graph timeout 45s, got %s", cfg.GraphTimeout)
	}
}
