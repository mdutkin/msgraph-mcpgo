package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
)

// DefineMCPTools returns all MCP tool definitions
func DefineMCPTools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name:        "search_emails",
			Description: "Search or filter emails. Returns metadata only (no body or attachments). To read the full body and attachments of a specific email, use get_email_details or read_email with the email id from these results. Note: 'query' ($search) and filter options (filter/isRead/hasAttachments/orderby) are mutually exclusive due to MS Graph API limitations.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Full-text search using KQL syntax ($search). Supports date ranges like 'received:2026-02-01..2026-03-15', field-specific like 'from:jane subject:workflow', and boolean operators 'AND', 'OR'. When set, filter/orderby/isRead/hasAttachments are ignored.",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
					"filter": map[string]interface{}{
						"type":        "string",
						"description": "Raw OData $filter expression (e.g. 'receivedDateTime ge 2024-01-01T00:00:00Z'). IMPORTANT: Do NOT use 'contains(body, ...)' or 'contains(subject, ...)' here; Microsoft Graph does not support text search via $filter. Use the 'query' parameter for full-text search instead. Combined with isRead/hasAttachments if also set.",
					},
					"folder": map[string]interface{}{
						"type":        "string",
						"description": "Mailbox folder to search. Well-known names: inbox, sentitems, drafts, deleteditems, junkemail. Defaults to all mail.",
					},
					"orderby": map[string]interface{}{
						"type":        "string",
						"description": "Sort order for results (ignored when query/$search is set)",
						"enum":        []string{"receivedDateTime desc", "receivedDateTime asc", "subject asc", "subject desc"},
					},
					"isRead": map[string]interface{}{
						"type":        "boolean",
						"description": "Filter by read status: true = read only, false = unread only. Omit to return both.",
					},
					"hasAttachments": map[string]interface{}{
						"type":        "boolean",
						"description": "Filter by attachment presence: true = with attachments, false = without. Omit to return both.",
					},
				},
				Required: []string{},
			},
		},
		{
			Name:        "get_calendar_events",
			Description: "Get calendar events in a time range. Supports both future and past events. Use start_time/end_time for precise windows (e.g. afternoon only, or a specific past week), or 'days' for a simple forward look-ahead.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"days": map[string]interface{}{
						"type":        "integer",
						"description": "Number of days to look ahead from now (ignored when start_time/end_time are provided)",
						"default":     7,
						"minimum":     1,
						"maximum":     90,
					},
					"start_time": map[string]interface{}{
						"type":        "string",
						"description": "Start of the query window in ISO-8601 format (e.g. '2026-03-19T12:00:00Z' for noon today). Can be in the past to search historical meetings. Overrides 'days' when set.",
					},
					"end_time": map[string]interface{}{
						"type":        "string",
						"description": "End of the query window in ISO-8601 format (e.g. '2026-03-19T18:00:00Z'). Required when start_time is set.",
					},
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Filter events by subject keyword (case-insensitive contains match). Useful for finding specific meetings.",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of events to return",
						"default":     20,
						"minimum":     1,
						"maximum":     100,
					},
					"timezone": map[string]interface{}{
						"type":        "string",
						"description": "IANA timezone for returned event times (e.g. 'Europe/Zurich', 'America/New_York'). Defaults to Europe/Zurich.",
						"default":     "Europe/Zurich",
					},
				},
				Required: []string{},
			},
		},
		{
			Name:        "list_recent_files",
			Description: "List recently modified OneDrive files",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of files to return",
						"default":     20,
						"minimum":     1,
						"maximum":     100,
					},
				},
				Required: []string{},
			},
		},
		{
			Name: "list_chats",
			Description: "List Teams chats the signed-in user is part of (1:1, group, and meeting chats). " +
				"Each chat includes member names so you can identify who is in the conversation. " +
				"Use the 'participant' filter to find chats with a specific person by name (ideal for 1:1 chats which often have no topic). " +
				"Use the 'topic' filter for named group chats. " +
				"Requires Chat.ReadBasic or Chat.Read permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of chats to return",
						"default":     20,
						"minimum":     1,
						"maximum":     50,
					},
					"topic": map[string]interface{}{
						"type":        "string",
						"description": "Filter chats by topic keyword (case-insensitive contains match). Best for named group chats.",
					},
					"participant": map[string]interface{}{
						"type":        "string",
						"description": "Filter chats by participant display name (case-insensitive contains match). Best for finding 1:1 chats with a specific person, e.g. 'John' or 'Jane Doe'.",
					},
				},
				Required: []string{},
			},
		},
		{
			Name: "get_chat_messages",
			Description: "Get messages from a specific Teams chat by chat ID. " +
				"Returns id, createdDateTime, from, body, and attachments for each message. " +
				"Attachments are extracted via the same pipeline as email attachments: " +
				"text-extractable formats (PDF, DOCX, XLSX, PPTX, HTML, CSV, TXT, MD, JSON, etc.) are returned as markdown in contentMarkdown, " +
				"truncated to 75 KB; binary formats (images, audio, video, archives, executables) and attachments larger than 4 MB return metadata only with a note. " +
				"Requires Chat.Read permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "string",
						"description": "The chat ID (obtain from list_chats)",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of messages to return (most recent first)",
						"default":     20,
						"minimum":     1,
						"maximum":     50,
					},
				},
				Required: []string{"chat_id"},
			},
		},
		{
			Name:        "search_users",
			Description: "Search for users in the organization by name or email address. Requires User.ReadBasic.All permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Name or email to search for (partial match supported)",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name: "get_meeting_transcript",
			Description: "Retrieve the transcript of a Teams online meeting. " +
				"Locate the meeting by title keyword, specific date, or set last_meeting=true to get the most recent one. " +
				"Returns transcript content in VTT format. " +
				"IMPORTANT: Only works for meetings organised by the authenticated user (due to /me/onlineMeetings). " +
				"For meetings organised by others, consider searching SharePoint for meeting notes or recordings instead. " +
				"Requires OnlineMeetingTranscript.Read.All permission on the app registration.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Keyword to match in the meeting title (partial, case-insensitive contains match).",
					},
					"date": map[string]interface{}{
						"type":        "string",
						"description": "Filter to a specific day in YYYY-MM-DD format (e.g. '2025-03-05').",
					},
					"last_meeting": map[string]interface{}{
						"type":        "boolean",
						"description": "If true, return the transcript of the most recent past Teams meeting. Can be combined with subject for 'most recent meeting about X'.",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Number of matching meetings to try before giving up (default 1, max 5). Useful when the first match may not have a transcript.",
						"default":     1,
						"minimum":     1,
						"maximum":     5,
					},
				},
				Required: []string{},
			},
		},
		{
			Name:        "schedule_meeting",
			Description: "Schedule a Microsoft Teams meeting with colleagues. Requires Calendar.ReadWrite permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Meeting subject",
					},
					"attendees": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of attendee email addresses",
					},
					"start_time": map[string]interface{}{
						"type":        "string",
						"description": "ISO-8601 start time (e.g., 2024-03-20T10:00:00Z)",
					},
					"end_time": map[string]interface{}{
						"type":        "string",
						"description": "ISO-8601 end time (e.g., 2024-03-20T11:00:00Z)",
					},
				},
				Required: []string{"subject", "attendees", "start_time", "end_time"},
			},
		},
		{
			Name: "search_sharepoint",
			Description: "Search Microsoft SharePoint and OneDrive for enterprise documents using KQL (Keyword Query Language). " +
				"KQL supports field-specific queries like 'filetype:pptx \"Data Fabric\"', date ranges, and boolean operators (AND, OR). " +
				"Returns: name (filename), webUrl, driveItemId, driveId, fileExtension, size, lastModifiedDateTime, createdBy, siteName, summary (snippet), resourceType (driveItem/listItem/site). " +
				"Use driveId + driveItemId with extract_file_content to download and parse file contents (DOCX, DOC, XLSX, PPTX, PDF, CSV). " +
				"For SharePoint site pages (.aspx), use get_sharepoint_page_content instead of extract_file_content to read the full page body content. " +
				"Results are validated before being returned: unresolvable items are dropped automatically, so every result should have a working driveId and driveItemId. " +
				"Requires Sites.Read.All permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "KQL search query. Supports free text, field queries (e.g. 'filetype:pptx', 'author:\"John\"'), date ranges, and boolean operators.",
					},
					"file_type": map[string]interface{}{
						"type":        "string",
						"description": "Convenience filter by file extension (e.g. 'pptx', 'docx', 'xlsx', 'pdf'). Appended as 'filetype:{value}' to the KQL query.",
					},
					"top": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return",
						"default":     10,
						"minimum":     1,
						"maximum":     50,
					},
				},
				Required: []string{"query"},
			},
		},
		{
			Name:        "send_teams_message",
			Description: "Sends a message to a specific Teams chat or channel. Requires ChatMessage.Send permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"chat_id": map[string]interface{}{
						"type":        "string",
						"description": "The ID of the chat to send the message to",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "The content of the message",
					},
				},
				Required: []string{"chat_id", "content"},
			},
		},
		{
			Name:        "get_user_org_chart",
			Description: "Uses the Graph API to retrieve the user's manager and direct reports. Requires User.Read.All permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"user_id": map[string]interface{}{
						"type":        "string",
						"description": "The email or ID of the user (omit to use the currently authenticated user)",
					},
				},
				Required: []string{},
			},
		},
		{
			Name:        "find_experts",
			Description: "Search for colleagues based on skills or recent projects.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"topic": map[string]interface{}{
						"type":        "string",
						"description": "The skill or topic to search for",
					},
				},
				Required: []string{"topic"},
			},
		},
		{
			Name: "extract_file_content",
			Description: "Download and extract text content from a OneDrive/SharePoint file (up to 1 GB raw; extracted markdown truncated to 75 KB). " +
				"This tool only works for OneDrive and SharePoint files. It does NOT work for Outlook emails; use get_email_details or read_email to read email content. " +
				"Returns the file content as structured markdown (tables → markdown tables, slides → sections) under `content`. " +
				"Binary formats (images, audio, video, archives, executables) and files larger than 4 MB return metadata only with a `note` (no `content` field); these are intentionally excluded from the inline response to keep MCP payloads within LLM context budgets. " +
				"For very large text files, the extracted content may be truncated at 75 KB (a `truncated` flag will be set to true). " +
				"Supports DOCX, DOC, XLSX, XLS, PPTX, PDF, CSV, HTML, EPUB, ZIP, Jupyter notebooks (.ipynb), RSS/Atom feeds, and plain text formats. " +
				"Provide EITHER (drive_id + item_id) from search_sharepoint/list_recent_files, OR web_url from search results. " +
				"If using web_url, the server will automatically resolve the drive_id and item_id.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"drive_id": map[string]interface{}{
						"type":        "string",
						"description": "The drive ID containing the file (from search_sharepoint driveId or list_recent_files driveId). Required if web_url is not provided.",
					},
					"item_id": map[string]interface{}{
						"type":        "string",
						"description": "The drive item ID of the file (from search_sharepoint driveItemId or list_recent_files id). Required if web_url is not provided.",
					},
					"web_url": map[string]interface{}{
						"type":        "string",
						"description": "The full web URL of the file (from search_sharepoint webUrl). Alternative to drive_id + item_id — the server will resolve the file automatically.",
					},
				},
				Required: []string{},
			},
		},
		{
			Name: "list_sharepoint_drives",
			Description: "List all document libraries (drives) for a SharePoint site. " +
				"Use this to discover drive IDs when search_sharepoint returns results with missing driveId. " +
				"The site URL can typically be inferred from file webUrls in search results. " +
				"Returns: drive ID, name, webUrl, and type for each document library. " +
				"Requires Sites.Read.All permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"site_url": map[string]interface{}{
						"type":        "string",
						"description": "The SharePoint site URL (e.g. 'https://contoso.sharepoint.com/sites/MySite')",
					},
				},
				Required: []string{"site_url"},
			},
		},
		{
			Name: "get_email_details",
			Description: "Read a single email message by ID, including its body and attachment metadata. " +
				"Use this after search_emails to retrieve the full content of a specific message. " +
				"Returns: id, subject, from, to, cc, receivedDateTime, body, bodyType, hasAttachments, attachments. " +
				"The body is truncated to 75 KB. Attachments are handled as follows: " +
				"text-extractable formats (PDF, DOCX, XLSX, PPTX, HTML, CSV, TXT, MD, JSON, etc.) are returned as markdown in contentMarkdown, " +
				"truncated to 75 KB; binary formats (images, audio, video, archives, executables) return metadata only with a note pointing to download_attachment; " +
				"attachments larger than 4 MB return metadata only. " +
				"To retrieve the raw bytes of any attachment, use download_attachment. " +
				"Requires Mail.Read or Mail.ReadWrite permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"email_id": map[string]interface{}{
						"type":        "string",
						"description": "The email message ID (from search_emails)",
					},
					"include_attachments": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to include attachments (default: true). Text-extractable attachments are returned as markdown; binary attachments return metadata only.",
						"default":     true,
					},
				},
				Required: []string{"email_id"},
			},
		},
		{
			Name: "read_email",
			Description: "Alias for get_email_details. Read a single email message by ID, including its body and attachments. " +
				"Use this after search_emails to retrieve the full content of a specific message. " +
				"Text-extractable attachments return markdown (truncated to 75 KB); binary attachments return metadata only — use download_attachment for raw bytes. " +
				"Requires Mail.Read or Mail.ReadWrite permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"email_id": map[string]interface{}{
						"type":        "string",
						"description": "The email message ID (from search_emails)",
					},
					"include_attachments": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to include attachments (default: true). Text-extractable attachments are returned as markdown; binary attachments return metadata only.",
						"default":     true,
					},
				},
				Required: []string{"email_id"},
			},
		},
		{
			Name: "download_attachment",
			Description: "Download a single email attachment by message ID and attachment ID. " +
				"Returns the attachment name, content type, size, and content as a base64 string. " +
				"Requires Mail.Read or Mail.ReadWrite permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"email_id": map[string]interface{}{
						"type":        "string",
						"description": "The email message ID containing the attachment",
					},
					"attachment_id": map[string]interface{}{
						"type":        "string",
						"description": "The attachment ID (from get_email_details/read_email)",
					},
				},
				Required: []string{"email_id", "attachment_id"},
			},
		},
		{
			Name: "send_email",
			Description: "Send an email on behalf of the authenticated user via Microsoft Graph. " +
				"Supports HTML and plain-text bodies, multiple recipients (to, cc, bcc), and optionally saves the message to Sent Items. " +
				"Requires Mail.Send (delegated) permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"to": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of recipient email addresses (required)",
					},
					"cc": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of CC email addresses (optional)",
					},
					"bcc": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of BCC email addresses (optional)",
					},
					"subject": map[string]interface{}{
						"type":        "string",
						"description": "Email subject line",
					},
					"body": map[string]interface{}{
						"type":        "string",
						"description": "Email body content (HTML by default, or plain text if body_type is set to 'text')",
					},
					"body_type": map[string]interface{}{
						"type":        "string",
						"description": "Body content type",
						"enum":        []string{"html", "text"},
						"default":     "html",
					},
					"save_to_sent_items": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to save the email in the Sent Items folder (default: true)",
						"default":     true,
					},
					"attachments": map[string]interface{}{
						"type":        "array",
						"description": "File attachments (max 3MB each). Each item needs 'name' and either 'content_base64' (base64-encoded content) OR 'drive_id'+'item_id' (to attach from OneDrive).",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name": map[string]interface{}{
									"type":        "string",
									"description": "Attachment filename (e.g. 'report.pdf')",
								},
								"content_base64": map[string]interface{}{
									"type":        "string",
									"description": "Base64-encoded file content (for inline attachments up to 3MB)",
								},
								"drive_id": map[string]interface{}{
									"type":        "string",
									"description": "OneDrive drive ID (use with item_id to attach a file from OneDrive)",
								},
								"item_id": map[string]interface{}{
									"type":        "string",
									"description": "OneDrive item ID (use with drive_id to attach a file from OneDrive)",
								},
								"content_type": map[string]interface{}{
									"type":        "string",
									"description": "MIME type (e.g. 'application/pdf'). Auto-detected if omitted.",
								},
							},
							"required": []string{"name"},
						},
					},
				},
				Required: []string{"to", "subject", "body"},
			},
		},
		{
			Name: "get_sharepoint_page_content",
			Description: "Read the full body content of a SharePoint site page (.aspx). " +
				"Unlike extract_file_content (which handles DOCX/PDF/XLSX/PPTX), this tool reads SharePoint wiki-style site pages " +
				"by fetching the page's canvas layout and extracting text from all web parts. " +
				"Use this when search_sharepoint finds a .aspx page (resourceType=listItem) whose snippet is too short. " +
				"Requires Sites.Read.All permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"site_url": map[string]interface{}{
						"type":        "string",
						"description": "The SharePoint site URL where the page lives (e.g. 'https://contoso.sharepoint.com/sites/MySite')",
					},
					"page_name": map[string]interface{}{
						"type":        "string",
						"description": "The page filename (e.g. 'Benefits.aspx'). The .aspx extension is added automatically if omitted.",
					},
					"search_title": map[string]interface{}{
						"type":        "string",
						"description": "Search for a page by title keyword (case-insensitive partial match). Use when you don't know the exact filename.",
					},
				},
				Required: []string{"site_url"},
			},
		},
		{
			Name: "get_user_availability",
			Description: "Check the free/busy availability of one or more users for a given time window. " +
				"Returns availability status (free, busy, tentative, out-of-office, working elsewhere) per time slot. " +
				"Does NOT return meeting details (subject, location) to protect privacy. " +
				"Useful for finding available meeting slots before scheduling. " +
				"Requires Calendars.Read permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"emails": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "List of email addresses to check availability for",
					},
					"start_time": map[string]interface{}{
						"type":        "string",
						"description": "Start of the availability window in ISO-8601 format (e.g. '2026-04-03T09:00:00Z')",
					},
					"end_time": map[string]interface{}{
						"type":        "string",
						"description": "End of the availability window in ISO-8601 format (e.g. '2026-04-03T18:00:00Z')",
					},
					"timezone": map[string]interface{}{
						"type":        "string",
						"description": "IANA timezone for the query (e.g. 'Europe/Zurich'). Defaults to Europe/Zurich.",
						"default":     "Europe/Zurich",
					},
					"interval": map[string]interface{}{
						"type":        "integer",
						"description": "Duration of each time slot in minutes for the availability view",
						"default":     30,
						"minimum":     5,
						"maximum":     1440,
					},
				},
				Required: []string{"emails", "start_time", "end_time"},
			},
		},
		{
			Name: "upload_file",
			Description: "Upload a file to the authenticated user's OneDrive. " +
				"Accepts base64-encoded file content (max 4MB). " +
				"If a file already exists at the target path, it will be replaced. " +
				"Returns the uploaded file metadata including webUrl. " +
				"Requires Files.ReadWrite (delegated) permission.",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"file_name": map[string]interface{}{
						"type":        "string",
						"description": "Name of the file to upload (e.g. 'report.docx', 'data.csv')",
					},
					"content_base64": map[string]interface{}{
						"type":        "string",
						"description": "File content encoded as base64 string",
					},
					"folder_path": map[string]interface{}{
						"type":        "string",
						"description": "Target folder path relative to OneDrive root (e.g. 'Documents/Reports'). Defaults to root if omitted.",
					},
				},
				Required: []string{"file_name", "content_base64"},
			},
		},
	}
}
