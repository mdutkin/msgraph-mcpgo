package mcp

import "testing"

func TestDefineMCPToolsExcludesAISummaries(t *testing.T) {
	tools := DefineMCPTools()
	if len(tools) != 21 {
		t.Fatalf("expected 21 tools after removing AI summaries, got %d", len(tools))
	}

	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name] = true
	}

	for _, removed := range []string{"get_user_summary", "summarize_teams_chat"} {
		if names[removed] {
			t.Errorf("removed AI summary tool %q is still registered", removed)
		}
	}

	for _, retained := range []string{"search_emails", "get_chat_messages", "search_sharepoint"} {
		if !names[retained] {
			t.Errorf("retained Graph tool %q is missing", retained)
		}
	}
}
