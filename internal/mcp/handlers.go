package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/fnfbraga/msgraph-mcpgo/internal/gemini"
	"github.com/fnfbraga/msgraph-mcpgo/internal/msgraph"
	"golang.org/x/sync/errgroup"
)

// handleGetUserSummary handles the get_user_summary tool
func (s *Server) handleGetUserSummary(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Extract parameters with defaults
	timeRange := "24h"
	if tr, ok := args["timeRange"].(string); ok {
		timeRange = tr
	}

	includeEmails := true
	if ie, ok := args["includeEmails"].(bool); ok {
		includeEmails = ie
	}

	includeFiles := true
	if if_, ok := args["includeFiles"].(bool); ok {
		includeFiles = if_
	}

	includeCalendar := true
	if ic, ok := args["includeCalendar"].(bool); ok {
		includeCalendar = ic
	}

	s.logger.Info().
		Str("time_range", timeRange).
		Bool("include_emails", includeEmails).
		Bool("include_files", includeFiles).
		Bool("include_calendar", includeCalendar).
		Msg("Getting user summary")

	// Get Graph client from context
	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch data concurrently.
	// Use a plain errgroup (no WithContext) so one failure doesn't cancel siblings.
	// This prevents the 1-failure-becomes-3 cascade that was tripping the circuit breaker.
	var g errgroup.Group

	var emails []*msgraph.Email
	var files []*msgraph.File
	var events []*msgraph.Event

	if includeEmails {
		g.Go(func() error {
			filter := s.buildTimeRangeFilter(timeRange)
			var fetchErr error
			emails, fetchErr = graphClient.GetEmails(ctx, 20, filter)
			return fetchErr
		})
	}

	if includeFiles {
		g.Go(func() error {
			var fetchErr error
			files, fetchErr = graphClient.GetRecentFiles(ctx, 20)
			return fetchErr
		})
	}

	if includeCalendar {
		g.Go(func() error {
			start := time.Now()
			end := start.AddDate(0, 0, 7)
			var fetchErr error
			events, fetchErr = graphClient.GetCalendarEvents(ctx, start, end, "")
			return fetchErr
		})
	}

	// Wait for all operations to complete
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Generate summary using Gemini
	summary, err := s.geminiAgent.SummarizeActivity(ctx, &gemini.ActivityData{
		Emails:    emails,
		Files:     files,
		Events:    events,
		TimeRange: timeRange,
	})

	if err != nil {
		return nil, err
	}

	s.logger.Info().
		Int("email_count", len(emails)).
		Int("file_count", len(files)).
		Int("event_count", len(events)).
		Msg("User summary generated successfully")

	return summary, nil
}

// handleSearchEmails handles the search_emails tool
func (s *Server) handleSearchEmails(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	opts := msgraph.SearchOptions{Top: 10}

	if q, ok := args["query"].(string); ok {
		opts.Query = q
	}
	if t, ok := args["top"].(float64); ok {
		opts.Top = int32(t)
	}
	if f, ok := args["filter"].(string); ok {
		lowerFilter := strings.ToLower(f)
		if strings.Contains(lowerFilter, "contains(body") || strings.Contains(lowerFilter, "contains(subject") {
			return nil, fmt.Errorf("invalid filter: Microsoft Graph API does not support 'contains' on body or subject in $filter. Please use the 'query' parameter for full-text search instead")
		}
		opts.Filter = f
	}
	if folder, ok := args["folder"].(string); ok {
		opts.Folder = folder
	}
	if orderby, ok := args["orderby"].(string); ok {
		opts.OrderBy = orderby
	}
	if isRead, ok := args["isRead"].(bool); ok {
		opts.IsRead = &isRead
	}
	if hasAttachments, ok := args["hasAttachments"].(bool); ok {
		opts.HasAttachments = &hasAttachments
	}

	// Sanitize $search query: rewrite common LLM mistakes (e.g. "received:2026-04-07")
	// into proper OData $filter expressions.
	if opts.Query != "" {
		cleanedQuery, extractedFilter := sanitizeEmailSearchQuery(opts.Query)
		if extractedFilter != "" {
			s.logger.Info().
				Str("original_query", opts.Query).
				Str("cleaned_query", cleanedQuery).
				Str("extracted_filter", extractedFilter).
				Msg("Sanitized email search query: rewrote field:value patterns to $filter")
			// Merge extracted filter with any existing filter
			if opts.Filter != "" {
				opts.Filter = opts.Filter + " and " + extractedFilter
			} else {
				opts.Filter = extractedFilter
			}
		}

		// Ensure the search query is enclosed in double quotes for MS Graph KQL
		// (unless it's already properly quoted).
		if cleanedQuery != "" {
			if !strings.HasPrefix(cleanedQuery, "\"") || !strings.HasSuffix(cleanedQuery, "\"") {
				safeQuery := strings.ReplaceAll(cleanedQuery, `"`, `\"`)
				opts.Query = fmt.Sprintf(`"%s"`, safeQuery)
			} else {
				opts.Query = cleanedQuery
			}
		} else {
			opts.Query = ""
		}
	}

	s.logger.Info().
		Str("query", opts.Query).
		Int32("top", opts.Top).
		Str("filter", opts.Filter).
		Str("folder", opts.Folder).
		Str("orderby", opts.OrderBy).
		Msg("Searching emails")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	emails, err := graphClient.SearchEmailsWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.logger.Info().
		Int("count", len(emails)).
		Msg("Emails retrieved successfully")

	return map[string]interface{}{
		"emails": emails,
		"count":  len(emails),
	}, nil
}

// handleGetEmailDetails handles the get_email_details and read_email tools
func (s *Server) handleGetEmailDetails(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	emailID, _ := args["email_id"].(string)
	if emailID == "" {
		return nil, fmt.Errorf("email_id is required")
	}

	includeAttachments := true
	if v, ok := args["include_attachments"].(bool); ok {
		includeAttachments = v
	}

	s.logger.Info().
		Str("email_id", emailID).
		Bool("include_attachments", includeAttachments).
		Msg("Reading email details")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	details, err := graphClient.GetEmailDetails(ctx, msgraph.GetEmailDetailsRequest{
		MessageID:           emailID,
		IncludeAttachments:  includeAttachments,
		AttachmentExtractor: s.attachmentExtractor,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read email: %w", err)
	}

	return map[string]interface{}{
		"email": details,
	}, nil
}

// handleDownloadAttachment handles the download_attachment tool
func (s *Server) handleDownloadAttachment(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	emailID, _ := args["email_id"].(string)
	attachmentID, _ := args["attachment_id"].(string)
	if emailID == "" || attachmentID == "" {
		return nil, fmt.Errorf("email_id and attachment_id are required")
	}

	s.logger.Info().
		Str("email_id", emailID).
		Str("attachment_id", attachmentID).
		Msg("Downloading email attachment")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	attachment, err := graphClient.DownloadAttachment(ctx, emailID, attachmentID)
	if err != nil {
		return nil, fmt.Errorf("failed to download attachment: %w", err)
	}

	return map[string]interface{}{
		"attachment": attachment,
	}, nil
}

// handleGetCalendarEvents handles the get_calendar_events tool
func (s *Server) handleGetCalendarEvents(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Extract parameters
	days := 7
	if d, ok := args["days"].(float64); ok {
		days = int(d)
	}

	top := int32(20)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	subjectFilter := ""
	if sf, ok := args["subject"].(string); ok {
		subjectFilter = sf
	}

	// User's IANA timezone (e.g. "Europe/Zurich") — tells Graph to return
	// event times already localised to this timezone.
	timezone := ""
	if tz, ok := args["timezone"].(string); ok {
		timezone = tz
	}

	// Determine time window: explicit start_time/end_time override "days"
	var start, end time.Time
	explicitRange := false

	if startStr, ok := args["start_time"].(string); ok && startStr != "" {
		// Normalize: LLMs sometimes omit the timezone offset.
		startStr = normalizeDateTime(startStr, timezone)
		parsed, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return nil, fmt.Errorf("invalid start_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T00:00:00Z'): %w", err)
		}
		start = parsed
		explicitRange = true

		if endStr, ok := args["end_time"].(string); ok && endStr != "" {
			endStr = normalizeDateTime(endStr, timezone)
			parsed, err := time.Parse(time.RFC3339, endStr)
			if err != nil {
				return nil, fmt.Errorf("invalid end_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T18:00:00Z'): %w", err)
			}
			end = parsed
		} else {
			// Default: start + days
			end = start.AddDate(0, 0, days)
		}
	} else {
		// Legacy behaviour: from now, N days ahead
		start = time.Now()
		end = start.AddDate(0, 0, days)
	}

	s.logger.Info().
		Int("days", days).
		Int32("top", top).
		Str("subject_filter", subjectFilter).
		Bool("explicit_range", explicitRange).
		Time("start", start).
		Time("end", end).
		Msg("Getting calendar events")

	// Get Graph client from context
	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch events using the time window
	events, err := graphClient.GetCalendarEvents(ctx, start, end, timezone)
	if err != nil {
		return nil, err
	}

	// Apply client-side subject filter if provided
	if subjectFilter != "" {
		needle := strings.ToLower(subjectFilter)
		filtered := make([]*msgraph.Event, 0, len(events))
		for _, e := range events {
			if strings.Contains(strings.ToLower(e.Subject), needle) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	// Apply top limit
	if int32(len(events)) > top {
		events = events[:top]
	}

	s.logger.Info().
		Int("count", len(events)).
		Msg("Calendar events retrieved successfully")

	return map[string]interface{}{
		"events": events,
		"count":  len(events),
	}, nil
}

// handleListRecentFiles handles the list_recent_files tool
func (s *Server) handleListRecentFiles(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Extract parameters
	top := int32(20)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	s.logger.Info().
		Int32("top", top).
		Msg("Listing recent files")

	// Get Graph client from context
	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch recent files
	files, err := graphClient.GetRecentFiles(ctx, top)
	if err != nil {
		return nil, err
	}

	s.logger.Info().
		Int("count", len(files)).
		Msg("Recent files retrieved successfully")

	return map[string]interface{}{
		"files": files,
		"count": len(files),
	}, nil
}

// handleGetMeetingTranscript handles the get_meeting_transcript tool
func (s *Server) handleGetMeetingTranscript(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	opts := msgraph.TranscriptOptions{Top: 1}

	if subject, ok := args["subject"].(string); ok {
		opts.Subject = subject
	}
	if dateStr, ok := args["date"].(string); ok && dateStr != "" {
		if t, err := time.Parse("2006-01-02", dateStr); err == nil {
			opts.Date = &t
		}
	}
	if lastMeeting, ok := args["last_meeting"].(bool); ok {
		opts.LastMeeting = lastMeeting
	}
	if top, ok := args["top"].(float64); ok {
		opts.Top = int32(top)
	}

	s.logger.Info().
		Str("subject", opts.Subject).
		Bool("last_meeting", opts.LastMeeting).
		Msg("Getting meeting transcript")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	transcript, err := graphClient.GetMeetingTranscript(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.logger.Info().
		Str("meeting_subject", transcript.MeetingSubject).
		Int("content_length", len(transcript.Content)).
		Msg("Meeting transcript retrieved successfully")

	return map[string]interface{}{
		"transcript": transcript,
	}, nil
}

// handleListChats handles the list_chats tool
func (s *Server) handleListChats(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	top := int32(20)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	topicFilter := ""
	if t, ok := args["topic"].(string); ok {
		topicFilter = t
	}

	participantFilter := ""
	if p, ok := args["participant"].(string); ok {
		participantFilter = p
	}

	s.logger.Info().Int32("top", top).Str("topic", topicFilter).Str("participant", participantFilter).Msg("Listing chats")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	chatList, err := graphClient.ListChats(ctx, top, topicFilter, participantFilter)
	if err != nil {
		return nil, err
	}

	s.logger.Info().Int("count", len(chatList)).Msg("Chats retrieved successfully")

	return map[string]interface{}{
		"chats": chatList,
		"count": len(chatList),
	}, nil
}

// handleGetChatMessages handles the get_chat_messages tool
func (s *Server) handleGetChatMessages(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	chatID, _ := args["chat_id"].(string)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}

	top := int32(20)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	s.logger.Info().Str("chat_id", chatID).Int32("top", top).Msg("Getting chat messages")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	messages, err := graphClient.GetChatMessages(ctx, msgraph.GetChatMessagesRequest{
		ChatID:              chatID,
		Top:                 top,
		AttachmentExtractor: s.attachmentExtractor,
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info().Int("count", len(messages)).Msg("Chat messages retrieved successfully")

	return map[string]interface{}{
		"messages": messages,
		"count":    len(messages),
	}, nil
}

// handleSearchUsers handles the search_users tool
func (s *Server) handleSearchUsers(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	top := int32(10)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	s.logger.Info().
		Str("query", query).
		Int32("top", top).
		Msg("Searching users")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	users, err := graphClient.SearchUsers(ctx, query, top)
	if err != nil {
		return nil, err
	}

	s.logger.Info().
		Int("count", len(users)).
		Msg("Users retrieved successfully")

	return map[string]interface{}{
		"users": users,
		"count": len(users),
	}, nil
}

// buildTimeRangeFilter builds an OData filter for the given time range
func (s *Server) buildTimeRangeFilter(timeRange string) string {
	var cutoff time.Time

	switch timeRange {
	case "24h":
		cutoff = time.Now().Add(-24 * time.Hour)
	case "7d":
		cutoff = time.Now().Add(-7 * 24 * time.Hour)
	case "30d":
		cutoff = time.Now().Add(-30 * 24 * time.Hour)
	default:
		cutoff = time.Now().Add(-24 * time.Hour)
	}

	return fmt.Sprintf("receivedDateTime ge %s", cutoff.Format(time.RFC3339))
}

// handleResourceRead handles reading a resource
func (s *Server) handleResourceRead(ctx context.Context, uri string) (interface{}, error) {
	s.logger.Info().
		Str("uri", uri).
		Msg("Reading resource")

	// Get Graph client from context
	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Parse URI and route to appropriate handler
	switch uri {
	case "msgraph://emails":
		emails, err := graphClient.GetEmails(ctx, 50, "")
		if err != nil {
			return nil, err
		}
		// Convert to JSON
		data, err := json.Marshal(emails)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal emails: %w", err)
		}
		return map[string]interface{}{
			"uri":      uri,
			"mimeType": "application/json",
			"text":     string(data),
		}, nil

	case "msgraph://files":
		files, err := graphClient.GetRecentFiles(ctx, 50)
		if err != nil {
			return nil, err
		}
		// Convert to JSON
		data, err := json.Marshal(files)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal files: %w", err)
		}
		return map[string]interface{}{
			"uri":      uri,
			"mimeType": "application/json",
			"text":     string(data),
		}, nil

	case "msgraph://calendar":
		start := time.Now()
		end := start.AddDate(0, 0, 30)
		events, err := graphClient.GetCalendarEvents(ctx, start, end, "")
		if err != nil {
			return nil, err
		}
		// Convert to JSON
		data, err := json.Marshal(events)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal events: %w", err)
		}
		return map[string]interface{}{
			"uri":      uri,
			"mimeType": "application/json",
			"text":     string(data),
		}, nil

	default:
		return nil, fmt.Errorf("unknown resource URI: %s", uri)
	}
}

// handleScheduleMeeting handles the schedule_meeting tool
func (s *Server) handleScheduleMeeting(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	subject, _ := args["subject"].(string)
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}
	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr == "" {
		return nil, fmt.Errorf("start_time is required")
	}
	endTimeStr, _ := args["end_time"].(string)
	if endTimeStr == "" {
		return nil, fmt.Errorf("end_time is required")
	}

	// Parse attendees list
	var attendees []string
	if attendeesRaw, ok := args["attendees"].([]interface{}); ok {
		for _, a := range attendeesRaw {
			if strA, ok := a.(string); ok {
				attendees = append(attendees, strA)
			}
		}
	}

	s.logger.Info().
		Str("subject", subject).
		Str("start_time", startTimeStr).
		Msg("Scheduling meeting")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Normalize: LLMs sometimes omit the timezone offset.
	startTimeStr = normalizeDateTime(startTimeStr, "")
	endTimeStr = normalizeDateTime(endTimeStr, "")

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		return nil, fmt.Errorf("invalid start_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T10:00:00Z'): %w", err)
	}
	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		return nil, fmt.Errorf("invalid end_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T11:00:00Z'): %w", err)
	}

	event, err := graphClient.ScheduleMeeting(ctx, subject, attendees, startTime, endTime)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"event": event,
	}, nil
}

// handleSearchSharepoint handles the search_sharepoint tool
func (s *Server) handleSearchSharepoint(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// Append file_type filter as KQL
	if fileType, ok := args["file_type"].(string); ok && fileType != "" {
		query = query + " filetype:" + fileType
	}

	top := int32(10)
	if t, ok := args["top"].(float64); ok {
		top = int32(t)
	}

	s.logger.Info().
		Str("query", query).
		Int32("top", top).
		Msg("Searching SharePoint")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	results, err := graphClient.SearchSharepoint(ctx, query, top)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"results": results,
		"count":   len(results),
	}, nil
}

// handleSummarizeTeamsChat handles the summarize_teams_chat tool
func (s *Server) handleSummarizeTeamsChat(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	chatID, _ := args["chat_id"].(string)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}

	msgCount := 50
	if val, ok := args["message_count"].(float64); ok {
		msgCount = int(val)
	}

	s.logger.Info().Str("chat_id", chatID).Int("message_count", msgCount).Msg("Summarizing Teams chat")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	messages, err := graphClient.GetChatMessages(ctx, msgraph.GetChatMessagesRequest{
		ChatID:              chatID,
		Top:                 int32(msgCount),
		AttachmentExtractor: s.attachmentExtractor,
	})
	if err != nil {
		return nil, err
	}

	summary, err := s.geminiAgent.SummarizeChat(ctx, messages)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"summary": summary,
	}, nil
}

// handleSendTeamsMessage handles the send_teams_message tool
func (s *Server) handleSendTeamsMessage(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	chatID, _ := args["chat_id"].(string)
	if chatID == "" {
		return nil, fmt.Errorf("chat_id is required")
	}
	content, _ := args["content"].(string)
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}

	s.logger.Info().Str("chat_id", chatID).Msg("Sending Teams message")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	err = graphClient.SendTeamsMessage(ctx, chatID, content)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "success",
	}, nil
}

// handleGetUserOrgChart handles the get_user_org_chart tool
func (s *Server) handleGetUserOrgChart(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	userID := "me"
	if uid, ok := args["user_id"].(string); ok && uid != "" {
		userID = uid
	}

	s.logger.Info().Str("user_id", userID).Msg("Getting org chart")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	chart, err := graphClient.GetUserOrgChart(ctx, userID)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"orgChart": chart,
	}, nil
}

// handleFindExperts handles the find_experts tool
func (s *Server) handleFindExperts(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	topic, _ := args["topic"].(string)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}

	s.logger.Info().Str("topic", topic).Msg("Finding experts")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	experts, err := graphClient.FindExperts(ctx, topic)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"experts": experts,
		"count":   len(experts),
	}, nil
}

// maxFileSize is the maximum file size allowed for content extraction (1 GB).
const maxFileSize = 1024 * 1024 * 1024

// loopConvertedFilename is the synthetic filename used when the Graph API
// converts a .loop / .fluid file to HTML on the server side. It lets the
// attachment extractor route the resulting bytes through the standard HTML
// markdown path.
const loopConvertedFilename = "loopfile.html"

// handleExtractFileContent handles the extract_file_content tool
func (s *Server) handleExtractFileContent(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	driveID, _ := args["drive_id"].(string)
	itemID, _ := args["item_id"].(string)
	webURL, _ := args["web_url"].(string)
	if webURL != "" {
		webURL = normalizeWebURL(webURL)
	}

	s.logger.Info().
		Str("drive_id", driveID).
		Str("item_id", itemID).
		Str("web_url", webURL).
		Msg("Extracting file content")

	if webURL != "" && !isValidSharePointOrOneDriveURL(webURL) {
		return nil, fmt.Errorf("web_url %q is not a valid OneDrive/SharePoint file URL. "+
			"This tool only supports OneDrive and SharePoint files. "+
			"To read an email, use get_email_details or read_email with the email id", webURL)
	}

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve driveID and itemID from web_url if not provided directly
	if (driveID == "" || itemID == "") && webURL != "" {
		s.logger.Info().Str("web_url", webURL).Msg("Resolving file from web URL")
		resolved, resolveErr := graphClient.ResolveFileByWebURL(ctx, webURL)
		if resolveErr != nil {
			return nil, fmt.Errorf("failed to resolve file from web_url %q: %w. "+
				"Suggestion: try using list_sharepoint_drives to find the correct drive ID, "+
				"then use extract_file_content with drive_id + item_id", webURL, resolveErr)
		}
		driveID = resolved.DriveId
		itemID = resolved.ID
		s.logger.Info().
			Str("resolved_drive_id", driveID).
			Str("resolved_item_id", itemID).
			Msg("Resolved file from web URL")
	}

	if driveID == "" || itemID == "" {
		return nil, fmt.Errorf("either (drive_id + item_id) or web_url is required. " +
			"Suggestion: use search_sharepoint to find files and get their driveId + driveItemId, " +
			"or provide the webUrl from search results")
	}

	// 1. Get file metadata (name, size) to validate before downloading
	file, err := graphClient.GetDriveItem(ctx, driveID, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to get file metadata: %w. "+
			"Suggestion: the item may be on a different drive. Use list_sharepoint_drives to "+
			"find available drives for the SharePoint site, or try web_url instead", err)
	}

	if file.Size > maxFileSize {
		return nil, fmt.Errorf("file too large: %s is %d bytes (max %d bytes / 1 GB)",
			file.Name, file.Size, maxFileSize)
	}

	s.logger.Info().
		Str("filename", file.Name).
		Int64("size", file.Size).
		Str("mime_type", file.MimeType).
		Msg("File metadata retrieved, downloading content")

	// 2. Download and extract the file content via the shared attachment
	// extractor. The extractor classifies the file, converts text formats to
	// markdown (truncated to the configured limit), and returns a `note` for
	// binary / oversize / unsupported / failed cases.
	//
	// Microsoft Loop files (.loop/.fluid) cannot be parsed locally, so the
	// Graph API's ?format=html server-side conversion is used and the
	// resulting HTML is fed back into the extractor with a synthetic filename.
	ext := strings.ToLower(filepath.Ext(file.Name))
	var data []byte
	var effectiveName string
	var dlErr error

	if ext == ".loop" || ext == ".fluid" {
		data, dlErr = graphClient.DownloadFileAsHTML(ctx, driveID, itemID)
		effectiveName = loopConvertedFilename
		if dlErr != nil {
			return nil, fmt.Errorf("failed to download %s as HTML: %w", file.Name, dlErr)
		}
	} else {
		data, dlErr = graphClient.DownloadFileContentWithRecovery(ctx, driveID, itemID, file.WebURL)
		effectiveName = file.Name
		if dlErr != nil {
			return nil, fmt.Errorf("failed to download file: %w", dlErr)
		}
	}

	markdown, note, kind := s.attachmentExtractor.Extract(ctx, data, effectiveName, file.MimeType)

	// Binary / oversize / unsupported files: return metadata + note instead
	// of failing the call. Genuine extraction failures (KindFailed) still
	// return an error so callers learn about broken documents.
	if kind == attachments.KindFailed {
		return nil, fmt.Errorf("failed to extract content from %s: %s", file.Name, note)
	}

	truncated := kind == attachments.KindText && note != ""
	contentProvided := markdown != ""

	s.logger.Info().
		Str("filename", file.Name).
		Str("kind", string(kind)).
		Int("content_length", len(markdown)).
		Bool("truncated", truncated).
		Bool("content_provided", contentProvided).
		Msg("File content extracted successfully")

	result := map[string]interface{}{
		"filename":        file.Name,
		"size":            file.Size,
		"mimeType":        file.MimeType,
		"kind":            string(kind),
		"truncated":       truncated,
		"contentProvided": contentProvided,
	}
	if contentProvided {
		result["content"] = markdown
	}
	if note != "" {
		result["note"] = note
	}
	return result, nil
}

// handleListSharePointDrives handles the list_sharepoint_drives tool
func (s *Server) handleListSharePointDrives(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	siteURL, _ := args["site_url"].(string)
	if siteURL == "" {
		return nil, fmt.Errorf("site_url is required")
	}

	s.logger.Info().Str("site_url", siteURL).Msg("Listing SharePoint drives")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	drives, siteName, err := graphClient.ListSharePointDrives(ctx, siteURL)
	if err != nil {
		return nil, fmt.Errorf("failed to list drives: %w. "+
			"Suggestion: verify the site URL format (e.g. 'https://contoso.sharepoint.com/sites/MySite'). "+
			"You can find the site URL from the webUrl of search_sharepoint results", err)
	}

	s.logger.Info().
		Str("site_name", siteName).
		Int("drive_count", len(drives)).
		Msg("SharePoint drives listed successfully")

	return map[string]interface{}{
		"siteName": siteName,
		"drives":   drives,
		"count":    len(drives),
	}, nil
}

// handleGetSharePointPageContent handles the get_sharepoint_page_content tool
func (s *Server) handleGetSharePointPageContent(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	siteURL, _ := args["site_url"].(string)
	if siteURL == "" {
		return nil, fmt.Errorf("site_url is required")
	}

	pageName, _ := args["page_name"].(string)
	searchTitle, _ := args["search_title"].(string)

	if pageName == "" && searchTitle == "" {
		return nil, fmt.Errorf("at least one of page_name or search_title is required")
	}

	s.logger.Info().
		Str("site_url", siteURL).
		Str("page_name", pageName).
		Str("search_title", searchTitle).
		Msg("Getting SharePoint page content")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	page, notFound, err := graphClient.GetSharePointPageContent(ctx, siteURL, pageName, searchTitle)
	if err != nil {
		return nil, err
	}

	if notFound != nil {
		// Build a helpful list of available pages for the AI to choose from
		type pageEntry struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		}
		available := make([]pageEntry, 0, len(notFound.Available))
		for _, p := range notFound.Available {
			available = append(available, pageEntry{Name: p.Name, Title: p.Title})
		}

		return map[string]interface{}{
			"found":           false,
			"message":         fmt.Sprintf("No page found matching page_name=%q or search_title=%q. See the available pages below and retry with an exact page_name from the list.", notFound.PageName, notFound.SearchTitle),
			"available_pages": available,
		}, nil
	}

	return map[string]interface{}{
		"page": page,
	}, nil
}

// handleSendEmail handles the send_email tool
func (s *Server) handleSendEmail(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Parse required "to" recipients
	var to []string
	if toRaw, ok := args["to"].([]interface{}); ok {
		for _, v := range toRaw {
			if s, ok := v.(string); ok {
				to = append(to, s)
			}
		}
	}
	if len(to) == 0 {
		return nil, fmt.Errorf("at least one 'to' recipient is required")
	}

	subject, _ := args["subject"].(string)
	if subject == "" {
		return nil, fmt.Errorf("subject is required")
	}

	body, _ := args["body"].(string)
	if body == "" {
		return nil, fmt.Errorf("body is required")
	}

	// Optional fields
	bodyType := "html"
	if bt, ok := args["body_type"].(string); ok && bt != "" {
		bodyType = bt
	}

	saveToSentItems := true
	if sts, ok := args["save_to_sent_items"].(bool); ok {
		saveToSentItems = sts
	}

	var cc []string
	if ccRaw, ok := args["cc"].([]interface{}); ok {
		for _, v := range ccRaw {
			if s, ok := v.(string); ok {
				cc = append(cc, s)
			}
		}
	}

	var bcc []string
	if bccRaw, ok := args["bcc"].([]interface{}); ok {
		for _, v := range bccRaw {
			if s, ok := v.(string); ok {
				bcc = append(bcc, s)
			}
		}
	}

	// Parse attachment descriptors
	type attachmentDescriptor struct {
		Name        string
		ContentType string
		ContentB64  string
		DriveID     string
		ItemID      string
	}
	var attachmentDescs []attachmentDescriptor
	if attachmentsRaw, ok := args["attachments"].([]interface{}); ok {
		for _, attRaw := range attachmentsRaw {
			attMap, ok := attRaw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := attMap["name"].(string)
			if name == "" {
				return nil, fmt.Errorf("attachment name is required")
			}
			desc := attachmentDescriptor{Name: name}
			desc.ContentType, _ = attMap["content_type"].(string)
			desc.ContentB64, _ = attMap["content_base64"].(string)
			desc.DriveID, _ = attMap["drive_id"].(string)
			desc.ItemID, _ = attMap["item_id"].(string)
			attachmentDescs = append(attachmentDescs, desc)
		}
	}

	s.logger.Info().
		Strs("to", to).
		Str("subject", subject).
		Str("body_type", bodyType).
		Int("attachments", len(attachmentDescs)).
		Msg("Sending email")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve attachments: decode base64 or download from OneDrive
	const maxAttachmentSize = 3 * 1024 * 1024 // 3MB Graph API limit per inline attachment
	var attachments []msgraph.EmailAttachment
	for _, desc := range attachmentDescs {
		att := msgraph.EmailAttachment{
			Name:        desc.Name,
			ContentType: desc.ContentType,
		}

		if desc.ContentB64 != "" {
			// Inline base64 content
			decoded, decErr := decodeBase64Flexible(desc.ContentB64)
			if decErr != nil {
				return nil, fmt.Errorf("invalid base64 content for attachment %q: %w", desc.Name, decErr)
			}
			att.ContentBytes = decoded
		} else if desc.DriveID != "" && desc.ItemID != "" {
			// Download from OneDrive
			content, dlErr := graphClient.DownloadFileContent(ctx, desc.DriveID, desc.ItemID)
			if dlErr != nil {
				return nil, fmt.Errorf("failed to download attachment %q from OneDrive: %w", desc.Name, dlErr)
			}
			att.ContentBytes = content
		} else {
			return nil, fmt.Errorf("attachment %q needs either content_base64 or drive_id+item_id", desc.Name)
		}

		att.Size = int64(len(att.ContentBytes))

		if att.Size > maxAttachmentSize {
			return nil, fmt.Errorf("attachment %q is %d bytes, exceeding the 3MB limit for inline attachments", att.Name, att.Size)
		}

		attachments = append(attachments, att)
	}

	err = graphClient.SendEmail(ctx, msgraph.SendEmailRequest{
		To:              to,
		Cc:              cc,
		Bcc:             bcc,
		Subject:         subject,
		Body:            body,
		BodyType:        bodyType,
		SaveToSentItems: saveToSentItems,
		Attachments:     attachments,
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  "success",
		"message": "Email sent successfully",
	}, nil
}

// handleGetUserAvailability handles the get_user_availability tool
func (s *Server) handleGetUserAvailability(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// Parse emails
	var emails []string
	if emailsRaw, ok := args["emails"].([]interface{}); ok {
		for _, v := range emailsRaw {
			if email, ok := v.(string); ok {
				emails = append(emails, email)
			}
		}
	}
	if len(emails) == 0 {
		return nil, fmt.Errorf("at least one email address is required")
	}

	startTimeStr, _ := args["start_time"].(string)
	if startTimeStr == "" {
		return nil, fmt.Errorf("start_time is required")
	}
	endTimeStr, _ := args["end_time"].(string)
	if endTimeStr == "" {
		return nil, fmt.Errorf("end_time is required")
	}

	timezone := ""
	if tz, ok := args["timezone"].(string); ok {
		timezone = tz
	}

	// Normalize: LLMs sometimes omit the timezone offset.
	startTimeStr = normalizeDateTime(startTimeStr, timezone)
	endTimeStr = normalizeDateTime(endTimeStr, timezone)

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		return nil, fmt.Errorf("invalid start_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T09:00:00Z'): %w", err)
	}
	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		return nil, fmt.Errorf("invalid end_time format (expected ISO-8601/RFC3339, e.g. '2026-04-07T18:00:00Z'): %w", err)
	}

	interval := int32(30)
	if iv, ok := args["interval"].(float64); ok {
		interval = int32(iv)
	}

	s.logger.Info().
		Strs("emails", emails).
		Str("start_time", startTimeStr).
		Str("end_time", endTimeStr).
		Int32("interval", interval).
		Msg("Getting user availability")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	schedules, err := graphClient.GetSchedule(ctx, emails, startTime, endTime, timezone, interval)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"schedules": schedules,
		"count":     len(schedules),
	}, nil
}

// handleUploadFile handles the upload_file tool
func (s *Server) handleUploadFile(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	fileName, _ := args["file_name"].(string)
	if fileName == "" {
		return nil, fmt.Errorf("file_name is required")
	}

	contentB64, _ := args["content_base64"].(string)
	if contentB64 == "" {
		return nil, fmt.Errorf("content_base64 is required")
	}

	// Decode base64 content
	content, err := decodeBase64Flexible(contentB64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content: %w", err)
	}

	// Check size limit (4MB for simple upload)
	const maxUploadSize = 4 * 1024 * 1024
	if len(content) > maxUploadSize {
		return nil, fmt.Errorf("file size %d bytes exceeds the 4MB limit for simple upload", len(content))
	}

	folderPath := ""
	if fp, ok := args["folder_path"].(string); ok {
		folderPath = fp
	}

	s.logger.Info().
		Str("file_name", fileName).
		Str("folder_path", folderPath).
		Int("content_size", len(content)).
		Msg("Uploading file to OneDrive")

	graphClient, err := s.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	file, err := graphClient.UploadFile(ctx, folderPath, fileName, content)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":  "success",
		"message": fmt.Sprintf("File '%s' uploaded successfully", fileName),
		"file":    file,
	}, nil
}

// decodeBase64Flexible decodes a base64 string with or without padding.
func decodeBase64Flexible(s string) ([]byte, error) {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "<") {
		errStr := s
		if len(errStr) > 50 {
			errStr = errStr[:47] + "..."
		}
		return nil, fmt.Errorf("it looks like you passed a placeholder or variable name (e.g. %q). You must pass the actual base64 encoded string data, not a variable reference", errStr)
	}

	// Try standard encoding first (with padding)
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	// Try without padding (common from LLM outputs)
	return base64.RawStdEncoding.DecodeString(s)
}
