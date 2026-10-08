package msgraph

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// Email represents an email message
type Email struct {
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	ReceivedTime   time.Time `json:"receivedDateTime"`
	BodyPreview    string    `json:"bodyPreview"`
	HasAttachments bool      `json:"hasAttachments"`
}

// SearchOptions defines options for searching emails.
// Note: Query ($search) is mutually exclusive with Filter/OrderBy/IsRead/HasAttachments ($filter) —
// the MS Graph API does not allow combining $search and $filter on messages.
type SearchOptions struct {
	Query          string // Full-text search ($search)
	Top            int32
	Filter         string // Raw OData $filter expression
	Folder         string // Well-known name (inbox, sentitems, drafts, deleteditems, junkemail) or folder ID
	OrderBy        string // e.g. "receivedDateTime desc"
	IsRead         *bool  // nil = any; true = read only; false = unread only
	HasAttachments *bool  // nil = any; true = with attachments; false = without
}

// GetEmails fetches recent emails with optional filter
func (c *Client) GetEmails(ctx context.Context, top int32, filter string) ([]*Email, error) {
	var messages []models.Messageable
	var err error

	err = c.executeWithResilience(ctx, "messages", func() error {
		// Build request configuration
		requestConfig := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
			QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
				Top: &top,
			},
		}

		if filter != "" {
			requestConfig.QueryParameters.Filter = &filter
		}

		// Fetch messages
		result, fetchErr := c.graphClient.Me().Messages().Get(ctx, requestConfig)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch messages: %w", fetchErr)
		}

		messages = result.GetValue()
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Convert to our Email type
	emails := make([]*Email, 0, len(messages))
	for _, msg := range messages {
		email := c.convertToEmail(msg)
		if email != nil {
			emails = append(emails, email)
		}
	}

	c.logger.Debug().
		Int("count", len(emails)).
		Str("filter_digest", observability.Redact(filter)).
		Msg("Fetched emails successfully")

	return emails, nil
}

// SearchEmails searches emails with a query
func (c *Client) SearchEmails(ctx context.Context, query string, top int32) ([]*Email, error) {
	var messages []models.Messageable
	var err error

	err = c.executeWithResilience(ctx, "messages_search", func() error {
		// Build search request
		requestConfig := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
			QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
				Top:    &top,
				Search: &query,
			},
		}

		// Fetch messages
		result, fetchErr := c.graphClient.Me().Messages().Get(ctx, requestConfig)
		if fetchErr != nil {
			return fmt.Errorf("failed to search messages: %w", fetchErr)
		}

		messages = result.GetValue()
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Convert to our Email type
	emails := make([]*Email, 0, len(messages))
	for _, msg := range messages {
		email := c.convertToEmail(msg)
		if email != nil {
			emails = append(emails, email)
		}
	}

	c.logger.Debug().
		Int("count", len(emails)).
		Str("query_digest", observability.Redact(query)).
		Msg("Searched emails successfully")

	return emails, nil
}

// SearchEmailsWithOptions fetches emails with full filter, folder, sort, and search support.
// When opts.Query is set, $search is used and filter/orderby options are ignored (Graph API limitation).
func (c *Client) SearchEmailsWithOptions(ctx context.Context, opts SearchOptions) ([]*Email, error) {
	if opts.Top == 0 {
		opts.Top = 10
	}

	filter := buildEmailFilter(opts.Filter, opts.IsRead, opts.HasAttachments)

	var messages []models.Messageable
	err := c.executeWithResilience(ctx, "messages_search", func() error {
		var result models.MessageCollectionResponseable
		var fetchErr error

		if opts.Query != "" {
			// Full-text search — $filter and $orderby not supported alongside $search
			if opts.Folder != "" {
				cfg := &users.ItemMailFoldersItemMessagesRequestBuilderGetRequestConfiguration{
					QueryParameters: &users.ItemMailFoldersItemMessagesRequestBuilderGetQueryParameters{
						Top:    &opts.Top,
						Search: &opts.Query,
					},
				}
				result, fetchErr = c.graphClient.Me().MailFolders().ByMailFolderId(opts.Folder).Messages().Get(ctx, cfg)
			} else {
				cfg := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
					QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
						Top:    &opts.Top,
						Search: &opts.Query,
					},
				}
				result, fetchErr = c.graphClient.Me().Messages().Get(ctx, cfg)
			}
		} else {
			// Filter/list mode — supports $filter and $orderby
			if opts.Folder != "" {
				cfg := &users.ItemMailFoldersItemMessagesRequestBuilderGetRequestConfiguration{
					QueryParameters: &users.ItemMailFoldersItemMessagesRequestBuilderGetQueryParameters{
						Top: &opts.Top,
					},
				}
				if filter != "" {
					cfg.QueryParameters.Filter = &filter
				}
				if opts.OrderBy != "" {
					cfg.QueryParameters.Orderby = []string{opts.OrderBy}
				}
				result, fetchErr = c.graphClient.Me().MailFolders().ByMailFolderId(opts.Folder).Messages().Get(ctx, cfg)
			} else {
				cfg := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
					QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
						Top: &opts.Top,
					},
				}
				if filter != "" {
					cfg.QueryParameters.Filter = &filter
				}
				if opts.OrderBy != "" {
					cfg.QueryParameters.Orderby = []string{opts.OrderBy}
				}
				result, fetchErr = c.graphClient.Me().Messages().Get(ctx, cfg)
			}
		}

		if fetchErr != nil {
			return fmt.Errorf("failed to fetch messages: %w", fetchErr)
		}
		messages = result.GetValue()
		return nil
	})
	if err != nil {
		return nil, err
	}

	emails := make([]*Email, 0, len(messages))
	for _, msg := range messages {
		if email := c.convertToEmail(msg); email != nil {
			emails = append(emails, email)
		}
	}

	c.logger.Debug().
		Int("count", len(emails)).
		Str("query_digest", observability.Redact(opts.Query)).
		Str("filter_digest", observability.Redact(filter)).
		Str("folder", opts.Folder).
		Msg("Fetched emails with options")

	return emails, nil
}

// buildEmailFilter combines OData filter predicates with "and".
func buildEmailFilter(base string, isRead, hasAttachments *bool) string {
	var parts []string
	if base != "" {
		parts = append(parts, "("+base+")")
	}
	if isRead != nil {
		if *isRead {
			parts = append(parts, "isRead eq true")
		} else {
			parts = append(parts, "isRead eq false")
		}
	}
	if hasAttachments != nil {
		if *hasAttachments {
			parts = append(parts, "hasAttachments eq true")
		} else {
			parts = append(parts, "hasAttachments eq false")
		}
	}
	return strings.Join(parts, " and ")
}

// convertToEmail converts a Graph SDK message to our Email type
func (c *Client) convertToEmail(msg models.Messageable) *Email {
	if msg == nil {
		return nil
	}

	email := &Email{}

	if id := msg.GetId(); id != nil {
		email.ID = *id
	}

	if subject := msg.GetSubject(); subject != nil {
		email.Subject = *subject
	}

	if from := msg.GetFrom(); from != nil {
		if emailAddr := from.GetEmailAddress(); emailAddr != nil {
			if addr := emailAddr.GetAddress(); addr != nil {
				email.From = *addr
			}
		}
	}

	if receivedTime := msg.GetReceivedDateTime(); receivedTime != nil {
		email.ReceivedTime = *receivedTime
	}

	if bodyPreview := msg.GetBodyPreview(); bodyPreview != nil {
		email.BodyPreview = *bodyPreview
	}

	if hasAttachments := msg.GetHasAttachments(); hasAttachments != nil {
		email.HasAttachments = *hasAttachments
	}

	return email
}

// EmailAttachment represents a file attachment for an email.
type EmailAttachment struct {
	Name         string `json:"name"`
	ContentType  string `json:"contentType,omitempty"`
	ContentBytes []byte `json:"-"` // excluded from JSON serialisation
	Size         int64  `json:"size"`
}

// EmailDetailsAttachment represents an attachment returned by email detail readers.
type EmailDetailsAttachment struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ContentType     string `json:"contentType,omitempty"`
	Size            int64  `json:"size"`
	Type            string `json:"type"` // fileAttachment, referenceAttachment, itemAttachment
	ContentBase64   string `json:"contentBase64,omitempty"`
	ContentMarkdown string `json:"contentMarkdown,omitempty"`
	WebUrl          string `json:"webUrl,omitempty"`
	DriveId         string `json:"driveId,omitempty"`
	ItemId          string `json:"itemId,omitempty"`
	Note            string `json:"note,omitempty"`
}

// EmailDetails represents the full content of an email message.
type EmailDetails struct {
	ID             string                    `json:"id"`
	Subject        string                    `json:"subject"`
	From           string                    `json:"from"`
	To             []string                  `json:"to,omitempty"`
	Cc             []string                  `json:"cc,omitempty"`
	ReceivedTime   time.Time                 `json:"receivedDateTime"`
	Body           string                    `json:"body"`
	BodyType       string                    `json:"bodyType"` // text or html
	HasAttachments bool                      `json:"hasAttachments"`
	Attachments    []*EmailDetailsAttachment `json:"attachments,omitempty"`
}

// GetEmailDetailsRequest controls what is returned by GetEmailDetails.
type GetEmailDetailsRequest struct {
	MessageID             string
	IncludeAttachments    bool
	InlineAttachmentLimit int64 // max bytes to include as base64 inline; 0 means use default
	// MaxBodyChars truncates the email body to at most this many bytes; the
	// remaining bytes are replaced with a truncation marker. Zero means no
	// truncation. Body truncation runs independently of attachment handling.
	MaxBodyChars int
	// AttachmentExtractor, when non-nil, replaces the default base64 inline
	// behaviour for file attachments. Text-extractable attachments are
	// returned as markdown (truncated per the extractor's limits) under
	// contentMarkdown. Binary and oversize attachments are returned as
	// metadata with a note pointing to download_attachment. When nil, the
	// historical behaviour (base64 for attachments <= InlineAttachmentLimit)
	// is preserved.
	AttachmentExtractor *attachments.Extractor
}

// defaultInlineAttachmentLimit is the maximum file attachment size returned inline
// as base64 in email detail responses. Larger attachments are omitted with a note.
const defaultInlineAttachmentLimit int64 = 3 * 1024 * 1024 // 3 MB

// defaultMaxBodyChars is the default truncation length for email bodies.
// Marketing HTML bodies can be hundreds of KB; trimming keeps individual
// MCP responses within reasonable context budgets.
const defaultMaxBodyChars = 75 * 1024 // 75 KB

// GetEmailDetails fetches a single email message by ID, including its body and
// optionally its attachments. For file attachments, small files (<= 3 MB by
// default) are returned as base64; larger ones are omitted with a note and their
// attachment ID is provided for separate retrieval via DownloadAttachment.
// Reference attachments (cloud files) are resolved to driveId + itemId when possible.
//
// When req.AttachmentExtractor is non-nil, file attachments are routed through
// it: text-extractable formats return markdown under contentMarkdown, and
// binary/oversize/unsupported attachments return metadata with a note. In
// that mode ContentBase64 is never populated; use download_attachment for raw
// bytes.
func (c *Client) GetEmailDetails(ctx context.Context, req GetEmailDetailsRequest) (*EmailDetails, error) {
	if req.MessageID == "" {
		return nil, fmt.Errorf("message ID is required")
	}
	limit := req.InlineAttachmentLimit
	if limit <= 0 {
		limit = defaultInlineAttachmentLimit
	}
	maxBody := req.MaxBodyChars
	if maxBody == 0 {
		maxBody = defaultMaxBodyChars
	}

	var result models.Messageable
	err := c.executeWithResilience(ctx, "email_details", func() error {
		cfg := &users.ItemMessagesMessageItemRequestBuilderGetRequestConfiguration{
			QueryParameters: &users.ItemMessagesMessageItemRequestBuilderGetQueryParameters{
				Expand: []string{"attachments"},
			},
		}
		msg, fetchErr := c.graphClient.Me().Messages().ByMessageId(req.MessageID).Get(ctx, cfg)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch email details: %w", fetchErr)
		}
		result = msg
		return nil
	})
	if err != nil {
		return nil, err
	}

	details := c.convertToEmailDetails(ctx, result, req.IncludeAttachments, limit, maxBody, req.AttachmentExtractor)
	return details, nil
}

// DownloadAttachment downloads a single file attachment by message and attachment ID.
// Note: the Microsoft Graph SDK for Go does not expose a dedicated content endpoint
// for message attachments, so this method retrieves the attachment metadata and returns
// its contentBytes. Attachments larger than ~3 MB may not include contentBytes; callers
// should handle the "attachment content not available" note from GetEmailDetails.
func (c *Client) DownloadAttachment(ctx context.Context, messageID, attachmentID string) ([]byte, error) {
	if messageID == "" || attachmentID == "" {
		return nil, fmt.Errorf("message ID and attachment ID are required")
	}

	var content []byte
	err := c.executeWithResilience(ctx, "email_attachment_download", func() error {
		att, fetchErr := c.graphClient.Me().Messages().ByMessageId(messageID).Attachments().ByAttachmentId(attachmentID).Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch attachment: %w", fetchErr)
		}
		if fileAtt, ok := att.(models.FileAttachmentable); ok {
			content = fileAtt.GetContentBytes()
			return nil
		}
		return fmt.Errorf("attachment %s is not a file attachment", attachmentID)
	})
	if err != nil {
		return nil, err
	}
	return content, nil
}

// convertToEmailDetails converts a Graph message to EmailDetails.
func (c *Client) convertToEmailDetails(ctx context.Context, msg models.Messageable, includeAttachments bool, inlineLimit int64, maxBodyChars int, extractor *attachments.Extractor) *EmailDetails {
	if msg == nil {
		return nil
	}

	details := &EmailDetails{}

	if id := msg.GetId(); id != nil {
		details.ID = *id
	}
	if subject := msg.GetSubject(); subject != nil {
		details.Subject = *subject
	}
	if from := msg.GetFrom(); from != nil {
		if emailAddr := from.GetEmailAddress(); emailAddr != nil {
			if addr := emailAddr.GetAddress(); addr != nil {
				details.From = *addr
			}
		}
	}
	if receivedTime := msg.GetReceivedDateTime(); receivedTime != nil {
		details.ReceivedTime = *receivedTime
	}
	if hasAttachments := msg.GetHasAttachments(); hasAttachments != nil {
		details.HasAttachments = *hasAttachments
	}

	if body := msg.GetBody(); body != nil {
		if content := body.GetContent(); content != nil {
			details.Body = truncateBody(*content, maxBodyChars)
		}
		if bodyType := body.GetContentType(); bodyType != nil {
			details.BodyType = strings.ToLower(bodyType.String())
		}
	}

	details.To = extractRecipientAddresses(msg.GetToRecipients())
	details.Cc = extractRecipientAddresses(msg.GetCcRecipients())

	if includeAttachments {
		details.Attachments = c.parseEmailAttachments(ctx, msg.GetAttachments(), inlineLimit, extractor)
	}

	return details
}

// truncateBody clips body to maxBytes, appending a marker indicating the
// original length. A non-positive maxBytes disables truncation.
func truncateBody(body string, maxBytes int) string {
	if maxBytes <= 0 || len(body) <= maxBytes {
		return body
	}
	return body[:maxBytes] + fmt.Sprintf("\n\n... [body truncated at %d bytes; original was %d bytes]",
		maxBytes, len(body))
}

// extractRecipientAddresses extracts email addresses from a slice of recipients.
func extractRecipientAddresses(recipients []models.Recipientable) []string {
	if recipients == nil {
		return nil
	}
	var addresses []string
	for _, r := range recipients {
		if r == nil {
			continue
		}
		if emailAddr := r.GetEmailAddress(); emailAddr != nil {
			if addr := emailAddr.GetAddress(); addr != nil {
				addresses = append(addresses, *addr)
			}
		}
	}
	return addresses
}

// parseEmailAttachments converts Graph attachments to EmailDetailsAttachment structs.
//
// When an extractor is supplied, file attachments are routed through it:
// text-extractable formats get their content as truncated markdown under
// contentMarkdown, while binary/oversize/unsupported attachments get
// metadata plus a note. ContentBase64 is left empty in that mode. When the
// extractor is nil, the historical behaviour applies: small files are
// inlined as base64 and oversize files get a note.
func (c *Client) parseEmailAttachments(ctx context.Context, atts []models.Attachmentable, inlineLimit int64, extractor *attachments.Extractor) []*EmailDetailsAttachment {
	if atts == nil {
		return nil
	}

	var result []*EmailDetailsAttachment
	for _, att := range atts {
		if att == nil {
			continue
		}
		info := &EmailDetailsAttachment{ID: ptrString(att.GetId())}
		if contentType := att.GetContentType(); contentType != nil {
			info.ContentType = *contentType
		}
		if name := att.GetName(); name != nil {
			info.Name = *name
		}
		if size := att.GetSize(); size != nil {
			info.Size = int64(*size)
		}

		// Determine type from odata.type discriminator.
		odataType := ""
		if entity, ok := att.(models.Entityable); ok {
			odataType = ptrString(entity.GetOdataType())
		}

		switch {
		case odataType == "#microsoft.graph.fileAttachment":
			info.Type = "fileAttachment"
			c.populateFileAttachment(ctx, att, info, inlineLimit, extractor)

		case odataType == "#microsoft.graph.referenceAttachment":
			info.Type = "referenceAttachment"
			if refAtt, ok := att.(models.ReferenceAttachmentable); ok {
				// The base Attachment interface has no sourceUrl getter in this SDK version;
				// cloud-file URLs are available in additionalData.
				webURL := extractStringFromAttachmentAdditionalData(refAtt, "sourceUrl")
				if webURL == "" {
					webURL = extractStringFromAttachmentAdditionalData(refAtt, "previewUrl")
				}
				if webURL == "" {
					webURL = extractStringFromAttachmentAdditionalData(refAtt, "webUrl")
				}
				info.WebUrl = webURL
				if webURL != "" {
					if resolved, resolveErr := c.ResolveFileByWebURL(context.Background(), webURL); resolveErr == nil && resolved != nil {
						info.DriveId = resolved.DriveId
						info.ItemId = resolved.ID
					} else {
						info.Note = fmt.Sprintf("could not resolve cloud attachment reference: %v", resolveErr)
					}
				}
			}

		case odataType == "#microsoft.graph.itemAttachment":
			info.Type = "itemAttachment"
			info.Note = "nested item attachments are not supported; retrieve via Microsoft Graph directly"

		default:
			info.Type = odataType
			if info.Type == "" {
				info.Type = "attachment"
			}
		}

		result = append(result, info)
	}

	return result
}

// populateFileAttachment handles the fileAttachment branch of
// parseEmailAttachments. When an extractor is provided, text-extractable
// attachments are converted to truncated markdown; otherwise the legacy
// base64-inline behaviour is preserved for backward compatibility.
func (c *Client) populateFileAttachment(ctx context.Context, att models.Attachmentable, info *EmailDetailsAttachment, inlineLimit int64, extractor *attachments.Extractor) {
	fileAtt, ok := att.(models.FileAttachmentable)
	if !ok {
		return
	}
	contentBytes := fileAtt.GetContentBytes()

	if extractor != nil {
		start := time.Now()
		md, note, kind := extractor.Extract(ctx, contentBytes, info.Name, info.ContentType)
		c.recordExtractionMetric(kind, note, time.Since(start))
		info.ContentMarkdown = md
		if note != "" {
			info.Note = note
		}
		return
	}

	if len(contentBytes) > 0 && int64(len(contentBytes)) <= inlineLimit {
		info.ContentBase64 = base64.StdEncoding.EncodeToString(contentBytes)
	} else if int64(len(contentBytes)) > inlineLimit {
		info.Note = fmt.Sprintf("attachment size %d exceeds inline limit %d; use download_attachment with attachment_id", len(contentBytes), inlineLimit)
	} else {
		info.Note = "attachment content not available; use download_attachment with attachment_id"
	}
}

// extractStringFromAttachmentAdditionalData extracts a string value from an
// attachment's AdditionalData map, handling both string and *string types.
func extractStringFromAttachmentAdditionalData(att models.Attachmentable, key string) string {
	if att == nil {
		return ""
	}
	entity, ok := att.(models.Entityable)
	if !ok {
		return ""
	}
	data := entity.GetAdditionalData()
	if data == nil {
		return ""
	}
	return extractString(data, key)
}

// ptrString returns the string value of a pointer or empty if nil.
func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// recordExtractionMetric emits the Prometheus counters/histograms for an
// attachment extraction. status is derived from kind: text+empty note means
// success, text+note means failure, all other kinds are skipped.
func (c *Client) recordExtractionMetric(kind attachments.Kind, note string, latency time.Duration) {
	if c.metrics == nil {
		return
	}
	kindLabel := string(kind)
	statusLabel := "skipped"
	if kind == attachments.KindText {
		if note == "" {
			statusLabel = "success"
		} else {
			statusLabel = "failed"
		}
	}
	c.metrics.AttachmentExtractionTotal.WithLabelValues(kindLabel, statusLabel).Inc()
	c.metrics.AttachmentExtractionLatency.WithLabelValues(kindLabel, statusLabel).Observe(latency.Seconds())
}

// SendEmailRequest defines the parameters for sending an email.
type SendEmailRequest struct {
	To              []string          // Required: recipient email addresses
	Cc              []string          // Optional: CC email addresses
	Bcc             []string          // Optional: BCC email addresses
	Subject         string            // Required: email subject
	Body            string            // Required: email body content
	BodyType        string            // "html" (default) or "text"
	SaveToSentItems bool              // Whether to save in Sent Items (default true)
	Attachments     []EmailAttachment // Optional: file attachments (max 3MB each for inline)
}

// SendEmail sends an email on behalf of the authenticated user.
// Requires Mail.Send (delegated) permission.
func (c *Client) SendEmail(ctx context.Context, req SendEmailRequest) error {
	if req.BodyType == "" {
		req.BodyType = "html"
	}

	err := c.executeWithResilience(ctx, "send_email", func() error {
		message := models.NewMessage()

		// Subject
		message.SetSubject(&req.Subject)

		// Body
		body := models.NewItemBody()
		body.SetContent(&req.Body)
		if req.BodyType == "text" {
			contentType := models.TEXT_BODYTYPE
			body.SetContentType(&contentType)
		} else {
			contentType := models.HTML_BODYTYPE
			body.SetContentType(&contentType)
		}
		message.SetBody(body)

		// To recipients
		toRecipients := make([]models.Recipientable, 0, len(req.To))
		for _, addr := range req.To {
			recipient := models.NewRecipient()
			emailAddr := models.NewEmailAddress()
			a := addr
			emailAddr.SetAddress(&a)
			recipient.SetEmailAddress(emailAddr)
			toRecipients = append(toRecipients, recipient)
		}
		message.SetToRecipients(toRecipients)

		// CC recipients
		if len(req.Cc) > 0 {
			ccRecipients := make([]models.Recipientable, 0, len(req.Cc))
			for _, addr := range req.Cc {
				recipient := models.NewRecipient()
				emailAddr := models.NewEmailAddress()
				a := addr
				emailAddr.SetAddress(&a)
				recipient.SetEmailAddress(emailAddr)
				ccRecipients = append(ccRecipients, recipient)
			}
			message.SetCcRecipients(ccRecipients)
		}

		// BCC recipients
		if len(req.Bcc) > 0 {
			bccRecipients := make([]models.Recipientable, 0, len(req.Bcc))
			for _, addr := range req.Bcc {
				recipient := models.NewRecipient()
				emailAddr := models.NewEmailAddress()
				a := addr
				emailAddr.SetAddress(&a)
				recipient.SetEmailAddress(emailAddr)
				bccRecipients = append(bccRecipients, recipient)
			}
			message.SetBccRecipients(bccRecipients)
		}

		// Attachments
		if len(req.Attachments) > 0 {
			attachments := make([]models.Attachmentable, 0, len(req.Attachments))
			for _, att := range req.Attachments {
				fileAttachment := models.NewFileAttachment()
				name := att.Name
				fileAttachment.SetName(&name)
				if att.ContentType != "" {
					ct := att.ContentType
					fileAttachment.SetContentType(&ct)
				}
				fileAttachment.SetContentBytes(att.ContentBytes)
				size := int32(len(att.ContentBytes))
				fileAttachment.SetSize(&size)
				attachments = append(attachments, fileAttachment)
			}
			message.SetAttachments(attachments)
		}

		// Build sendMail request body
		requestBody := users.NewItemSendMailPostRequestBody()
		requestBody.SetMessage(message)
		requestBody.SetSaveToSentItems(&req.SaveToSentItems)

		sendErr := c.graphClient.Me().SendMail().Post(ctx, requestBody, nil)
		if sendErr != nil {
			return fmt.Errorf("failed to send email: %w", sendErr)
		}

		return nil
	})

	if err == nil {
		c.logger.Info().
			Strs("to", req.To).
			Str("subject_digest", observability.Redact(req.Subject)).
			Msg("Email sent successfully")
	}

	return err
}
