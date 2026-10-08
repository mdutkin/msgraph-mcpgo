package mcp

import (
	"sort"
	"strings"
)

// graphScopePrefix is the resource every delegated Microsoft Graph permission
// is qualified by when requested as an OAuth scope.
const graphScopePrefix = "https://graph.microsoft.com/"

// toolScopes records the delegated Microsoft Graph permission each tool needs.
//
// It exists so that a missing permission is reported at startup rather than
// discovered as an opaque Graph authorization error on the first call. Getting
// consent wrong is the most likely way this service fails in a new tenant, and
// the failure is otherwise indistinguishable from a bug: Graph answers with a
// generic 403 that names neither the tool nor the permission.
//
// Keep this in step with the Graph calls in internal/msgraph. A tool whose
// requirements are not listed here is assumed to need nothing, so an omission
// weakens the check rather than breaking it.
var toolScopes = map[string][]string{
	// Mail. Reading a message, its details and its attachments are all one
	// permission; sending is separate because it is a write.
	"search_emails":       {"Mail.Read"},
	"get_email_details":   {"Mail.Read"},
	"read_email":          {"Mail.Read"},
	"download_attachment": {"Mail.Read"},
	"send_email":          {"Mail.Send"},

	// Calendar. getSchedule reads other people's free/busy, so Microsoft
	// requires the .Shared variant rather than Calendars.Read.
	"get_calendar_events":   {"Calendars.Read"},
	"get_user_availability": {"Calendars.Read.Shared"},
	"schedule_meeting":      {"Calendars.ReadWrite"},

	// Teams chat.
	"list_chats":         {"Chat.Read"},
	"get_chat_messages":  {"Chat.Read"},
	"send_teams_message": {"ChatMessage.Send"},

	// Meeting transcripts need both the meeting and the transcript.
	"get_meeting_transcript": {"OnlineMeetings.Read", "OnlineMeetingTranscript.Read.All"},

	// Files and SharePoint. search_sharepoint queries the driveItem, listItem
	// and site entity types, and Graph requires the permission for every type
	// requested, so it needs both.
	"list_recent_files":           {"Files.Read.All"},
	"extract_file_content":        {"Files.Read.All"},
	"upload_file":                 {"Files.ReadWrite"},
	"search_sharepoint":           {"Files.Read.All", "Sites.Read.All"},
	"list_sharepoint_drives":      {"Sites.Read.All"},
	"get_sharepoint_page_content": {"Sites.Read.All"},

	// Directory. Reading a manager or direct reports is not covered by
	// User.ReadBasic.All, which is limited to basic profile fields.
	"search_users":       {"User.ReadBasic.All"},
	"get_user_org_chart": {"User.Read.All"},
	"find_experts":       {"People.Read", "User.ReadBasic.All"},
}

// resourceScopes records the permissions each resource needs, for the same
// reason.
var resourceScopes = map[string][]string{
	"msgraph://emails":   {"Mail.Read"},
	"msgraph://files":    {"Files.Read.All"},
	"msgraph://calendar": {"Calendars.Read"},
}

// ToolScopes returns the permission each tool requires, as unqualified Graph
// permission names.
func ToolScopes() map[string][]string {
	out := make(map[string][]string, len(toolScopes))
	for tool, scopes := range toolScopes {
		out[tool] = append([]string(nil), scopes...)
	}
	return out
}

// RequiredScopes returns the fully qualified Graph scopes needed to serve the
// given tools and resources, with no duplicates.
//
// This is what makes a withheld tool also a withheld permission: deriving the
// requested scope set from what is actually exposed means the token obtained on
// a user's behalf cannot reach data no exposed tool can ask for.
func RequiredScopes(tools, resources []string) []string {
	needed := map[string]bool{}

	for _, tool := range tools {
		for _, scope := range toolScopes[tool] {
			needed[graphScopePrefix+scope] = true
		}
	}
	for _, resource := range resources {
		for _, scope := range resourceScopes[resource] {
			needed[graphScopePrefix+scope] = true
		}
	}

	// User.Read is always included: every deployment reads the signed-in
	// user's own profile to attribute requests, and it is the one permission
	// that needs no administrator.
	needed[graphScopePrefix+"User.Read"] = true

	out := make([]string, 0, len(needed))
	for scope := range needed {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// UnsatisfiedTools reports which of the given tools cannot work with the
// granted scope set, and which permission each one is missing.
//
// granted may be qualified or unqualified; both spellings of the same
// permission are treated as equal, because Entra accepts either in a scope
// request and an operator may have written it either way.
func UnsatisfiedTools(tools, granted []string) map[string][]string {
	have := map[string]bool{}
	for _, scope := range granted {
		have[normalizeScope(scope)] = true
	}

	out := map[string][]string{}
	for _, tool := range tools {
		var missing []string
		for _, scope := range toolScopes[tool] {
			if !have[normalizeScope(scope)] {
				missing = append(missing, scope)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out[tool] = missing
		}
	}
	return out
}

// normalizeScope strips the resource qualifier and lowercases, so
// "https://graph.microsoft.com/Mail.Read" and "mail.read" compare equal.
func normalizeScope(scope string) string {
	s := strings.TrimSpace(scope)
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	return strings.ToLower(s)
}
