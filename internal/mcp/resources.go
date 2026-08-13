package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// DefineMCPResources returns all MCP resource definitions
func DefineMCPResources() []mcp.Resource {
	return []mcp.Resource{
		{
			URI:         "msgraph://emails",
			Name:        "Recent Emails",
			Description: "List of recent emails from the user's mailbox",
			MIMEType:    "application/json",
		},
		{
			URI:         "msgraph://files",
			Name:        "Recent Files",
			Description: "List of recently modified files from the user's OneDrive",
			MIMEType:    "application/json",
		},
		{
			URI:         "msgraph://calendar",
			Name:        "Calendar Events",
			Description: "List of upcoming calendar events",
			MIMEType:    "application/json",
		},
	}
}
