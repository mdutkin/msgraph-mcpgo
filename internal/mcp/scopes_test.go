package mcp

import (
	"reflect"
	"sort"
	"testing"
)

// Every tool the build registers must have its permissions recorded, otherwise
// the startup check silently passes a tool it cannot vouch for.
func TestEveryToolHasARecordedPermission(t *testing.T) {
	for _, name := range KnownToolNames() {
		if _, ok := toolScopes[name]; !ok {
			t.Errorf("tool %q has no entry in toolScopes", name)
		}
	}
}

// And the reverse: an entry naming a tool that no longer exists is stale.
func TestNoStalePermissionEntries(t *testing.T) {
	known := map[string]bool{}
	for _, name := range KnownToolNames() {
		known[name] = true
	}
	for name := range toolScopes {
		if !known[name] {
			t.Errorf("toolScopes names %q, which is not a registered tool", name)
		}
	}
}

func TestEveryResourceHasARecordedPermission(t *testing.T) {
	for _, uri := range KnownResourceURIs() {
		if _, ok := resourceScopes[uri]; !ok {
			t.Errorf("resource %q has no entry in resourceScopes", uri)
		}
	}
}

// Deriving scopes from a mail-free policy must not request a mail permission:
// that is the whole point of deriving them.
func TestRequiredScopesFollowsWhatIsExposed(t *testing.T) {
	scopes := RequiredScopes(
		[]string{"get_calendar_events", "list_recent_files"},
		[]string{"msgraph://calendar"},
	)

	want := []string{
		"https://graph.microsoft.com/Calendars.Read",
		"https://graph.microsoft.com/Files.Read.All",
		"https://graph.microsoft.com/User.Read",
	}
	if !reflect.DeepEqual(scopes, want) {
		t.Fatalf("derived scopes = %v, want %v", scopes, want)
	}
}

// Exposing a mail tool must pull its permission in.
func TestRequiredScopesIncludesMailWhenAMailToolIsExposed(t *testing.T) {
	scopes := RequiredScopes([]string{"search_emails"}, nil)

	found := false
	for _, s := range scopes {
		if s == "https://graph.microsoft.com/Mail.Read" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Mail.Read missing from %v", scopes)
	}
}

// User.Read is always present: every deployment reads the caller's own profile.
func TestRequiredScopesAlwaysIncludesUserRead(t *testing.T) {
	scopes := RequiredScopes(nil, nil)
	if !reflect.DeepEqual(scopes, []string{"https://graph.microsoft.com/User.Read"}) {
		t.Fatalf("got %v", scopes)
	}
}

func TestRequiredScopesDeduplicates(t *testing.T) {
	// Three tools, all needing Files.Read.All.
	scopes := RequiredScopes([]string{"list_recent_files", "extract_file_content", "search_sharepoint"}, nil)

	seen := map[string]int{}
	for _, s := range scopes {
		seen[s]++
	}
	for scope, count := range seen {
		if count != 1 {
			t.Errorf("%s appears %d times", scope, count)
		}
	}
}

// The case that prompted this: a consent set with no mail, calendar or chat
// permission must be reported against every tool that needs one.
func TestUnsatisfiedToolsReportsTheRealConsentSet(t *testing.T) {
	granted := []string{
		"https://graph.microsoft.com/User.Read",
		"https://graph.microsoft.com/User.ReadBasic.All",
		"https://graph.microsoft.com/People.Read",
		"https://graph.microsoft.com/Files.Read.All",
		"https://graph.microsoft.com/GroupMember.Read.All",
	}

	unmet := UnsatisfiedTools(KnownToolNames(), granted)

	// These work with the set above and must not be reported.
	for _, tool := range []string{"list_recent_files", "extract_file_content", "search_users", "find_experts"} {
		if missing, reported := unmet[tool]; reported {
			t.Errorf("%s was reported as unusable, missing %v", tool, missing)
		}
	}

	// These cannot work and must be reported with the permission they need.
	expect := map[string]string{
		"search_emails":               "Mail.Read",
		"send_email":                  "Mail.Send",
		"get_calendar_events":         "Calendars.Read",
		"schedule_meeting":            "Calendars.ReadWrite",
		"list_chats":                  "Chat.Read",
		"send_teams_message":          "ChatMessage.Send",
		"upload_file":                 "Files.ReadWrite",
		"list_sharepoint_drives":      "Sites.Read.All",
		"get_sharepoint_page_content": "Sites.Read.All",
		"get_user_org_chart":          "User.Read.All",
	}
	for tool, want := range expect {
		missing, reported := unmet[tool]
		if !reported {
			t.Errorf("%s needs %s but was not reported", tool, want)
			continue
		}
		if !contains(missing, want) {
			t.Errorf("%s reported missing %v, expected to include %s", tool, missing, want)
		}
	}

	// search_sharepoint has Files.Read.All but not Sites.Read.All, and Graph
	// requires the permission for every entity type requested.
	if missing, reported := unmet["search_sharepoint"]; !reported || !contains(missing, "Sites.Read.All") {
		t.Errorf("search_sharepoint reported %v, expected Sites.Read.All missing", missing)
	}
}

// Qualified and unqualified spellings of a permission are the same permission.
func TestUnsatisfiedToolsAcceptsEitherScopeSpelling(t *testing.T) {
	for _, granted := range [][]string{
		{"https://graph.microsoft.com/Mail.Read"},
		{"Mail.Read"},
		{"mail.read"},
	} {
		if unmet := UnsatisfiedTools([]string{"search_emails"}, granted); len(unmet) != 0 {
			t.Errorf("granted %v was not recognised: %v", granted, unmet)
		}
	}
}

func TestUnsatisfiedToolsIsEmptyWhenEverythingIsGranted(t *testing.T) {
	all := RequiredScopes(KnownToolNames(), KnownResourceURIs())
	if unmet := UnsatisfiedTools(KnownToolNames(), all); len(unmet) != 0 {
		t.Fatalf("the derived set does not satisfy its own tools: %v", unmet)
	}
}

// ToolScopes must hand out a copy; a caller mutating it would corrupt the table.
func TestToolScopesReturnsACopy(t *testing.T) {
	first := ToolScopes()
	first["search_emails"][0] = "Mutated"

	if ToolScopes()["search_emails"][0] != "Mail.Read" {
		t.Fatal("the permission table was mutated through ToolScopes")
	}
}

func contains(haystack []string, needle string) bool {
	i := sort.SearchStrings(haystack, needle)
	return i < len(haystack) && haystack[i] == needle
}
