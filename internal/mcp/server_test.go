package mcp

import (
	"testing"
)

func TestExtractMCPMethod(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{"tools/list", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "tools/list"},
		{"initialize", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`, "initialize"},
		{"tools/call", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_emails"}}`, "tools/call"},
		{"empty body", ``, ""},
		{"invalid json", `not json`, ""},
		{"no method field", `{"jsonrpc":"2.0","id":1}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMCPMethod([]byte(tt.body))
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestIsMCPDiscoveryMethod(t *testing.T) {
	discoveryMethods := []string{
		"initialize",
		"notifications/initialized",
		"tools/list",
		"resources/list",
		"prompts/list",
	}

	for _, method := range discoveryMethods {
		t.Run(method, func(t *testing.T) {
			if !isMCPDiscoveryMethod(method) {
				t.Errorf("expected %q to be a discovery method", method)
			}
		})
	}

	protectedMethods := []string{
		"tools/call",
		"resources/read",
		"prompts/get",
		"ping",
		"",
	}

	for _, method := range protectedMethods {
		t.Run(method, func(t *testing.T) {
			if isMCPDiscoveryMethod(method) {
				t.Errorf("expected %q NOT to be a discovery method", method)
			}
		})
	}
}
