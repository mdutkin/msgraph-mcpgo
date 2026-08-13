package msgraph

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/microsoftgraph/msgraph-sdk-go/chats"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

// Chat represents a Teams chat conversation
type Chat struct {
	ID          string    `json:"id"`
	Topic       string    `json:"topic"`
	ChatType    string    `json:"chatType"`
	CreatedAt   time.Time `json:"createdDateTime"`
	LastMessage string    `json:"lastMessage,omitempty"`
	Members     []string  `json:"members,omitempty"`
}

// ChatMessage represents a message in a Teams chat. Attachments use the same
// extraction pipeline as email attachments (see internal/attachments) so the
// LLM receives markdown instead of raw base64 for text-extractable formats.
type ChatMessage struct {
	ID          string                    `json:"id"`
	CreatedAt   time.Time                 `json:"createdDateTime"`
	From        string                    `json:"from"`
	Body        string                    `json:"body"`
	Attachments []*EmailDetailsAttachment `json:"attachments,omitempty"`
}

// ListChats returns the chats the signed-in user is part of.
// Requires Chat.ReadBasic or Chat.Read (Delegated).
// Filters (applied client-side, case-insensitive contains):
//   - topicFilter: match on chat topic
//   - participantFilter: match on member display name (useful for 1:1 chats that have no topic)
//
// When participantFilter is set, the function uses server-side $filter=chatType eq 'oneOnOne'
// and pages through results (up to 200 chats) to find the 1:1 chat reliably.
func (c *Client) ListChats(ctx context.Context, top int32, topicFilter, participantFilter string) ([]*Chat, error) {
	if top == 0 {
		top = 20
	}

	var results []*Chat
	topicNeedle := strings.ToLower(topicFilter)
	participantNeedle := strings.ToLower(participantFilter)

	// When searching for a participant, page through more results because the
	// 1:1 chat may be far back. Use server-side chatType filter to narrow scope.
	const maxPagesWhenSearching = 4
	pageSize := top
	maxPages := 1
	if participantNeedle != "" {
		pageSize = 50 // larger pages when searching
		maxPages = maxPagesWhenSearching
	}

	err := c.executeWithResilience(ctx, "chats_list", func() error {
		var nextLink *string

		for page := 0; page < maxPages; page++ {
			var result models.ChatCollectionResponseable
			var fetchErr error

			if page == 0 {
				qp := &chats.ChatsRequestBuilderGetQueryParameters{
					Top:    &pageSize,
					Select: []string{"id", "topic", "chatType", "createdDateTime", "lastMessagePreview"},
					Expand: []string{"lastMessagePreview", "members"},
				}
				// Apply server-side chatType filter for 1:1 chat searches
				if participantNeedle != "" {
					filter := "chatType eq 'oneOnOne'"
					qp.Filter = &filter
				}
				cfg := &chats.ChatsRequestBuilderGetRequestConfiguration{
					QueryParameters: qp,
				}
				result, fetchErr = c.graphClient.Chats().Get(ctx, cfg)
			} else if nextLink != nil {
				// For subsequent pages, use WithUrl which encapsulates the full request URL
				result, fetchErr = c.graphClient.Chats().WithUrl(*nextLink).Get(ctx, nil)
			} else {
				break // no more pages
			}

			if fetchErr != nil {
				return fmt.Errorf("failed to list chats: %w", fetchErr)
			}

			for _, ch := range result.GetValue() {
				chat := convertChat(ch)

				// Apply client-side topic filter
				if topicNeedle != "" && !strings.Contains(strings.ToLower(chat.Topic), topicNeedle) {
					continue
				}

				// Apply client-side participant filter (match any member name)
				if participantNeedle != "" {
					matched := false
					for _, m := range chat.Members {
						if strings.Contains(strings.ToLower(m), participantNeedle) {
							matched = true
							break
						}
					}
					if !matched {
						continue
					}
				}

				results = append(results, chat)
			}

			// If we found a match when searching by participant, stop paging
			if participantNeedle != "" && len(results) > 0 {
				break
			}

			// Check for next page (skip link paging support)
			nextLink = nil
			if nl := result.GetOdataNextLink(); nl != nil && *nl != "" {
				nextLink = nl
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Int("count", len(results)).
		Msg("Listed chats successfully")

	return results, nil
}

// convertChat extracts field values from a Graph chat object into our Chat struct.
func convertChat(ch models.Chatable) *Chat {
	chat := &Chat{}
	if v := ch.GetId(); v != nil {
		chat.ID = *v
	}
	if v := ch.GetTopic(); v != nil {
		chat.Topic = *v
	}
	if v := ch.GetChatType(); v != nil {
		chat.ChatType = v.String()
	}
	if v := ch.GetCreatedDateTime(); v != nil {
		chat.CreatedAt = *v
	}
	if lm := ch.GetLastMessagePreview(); lm != nil {
		if body := lm.GetBody(); body != nil {
			if content := body.GetContent(); content != nil {
				chat.LastMessage = *content
			}
		}
	}
	// Extract member display names from expanded members
	for _, member := range ch.GetMembers() {
		if name := member.GetDisplayName(); name != nil && *name != "" {
			chat.Members = append(chat.Members, *name)
		}
	}
	return chat
}

// GetChatMessagesRequest controls what is returned by GetChatMessages.
type GetChatMessagesRequest struct {
	ChatID              string
	Top                 int32
	AttachmentExtractor *attachments.Extractor
}

// GetChatMessages returns messages from a specific chat. When the request
// includes an AttachmentExtractor, each message's attachments are fetched
// (via $expand=attachments) and routed through the same extraction pipeline
// used for email attachments: text-extractable formats return markdown,
// binary/oversize/unsupported attachments return metadata with a note.
// Requires Chat.Read (Delegated).
func (c *Client) GetChatMessages(ctx context.Context, req GetChatMessagesRequest) ([]*ChatMessage, error) {
	if req.ChatID == "" {
		return nil, fmt.Errorf("chat ID is required")
	}
	top := req.Top
	if top == 0 {
		top = 20
	}

	var results []*ChatMessage

	err := c.executeWithResilience(ctx, "chat_messages", func() error {
		cfg := &chats.ItemMessagesRequestBuilderGetRequestConfiguration{
			QueryParameters: &chats.ItemMessagesRequestBuilderGetQueryParameters{
				Top:     &top,
				Orderby: []string{"createdDateTime desc"},
				// NOTE: $select is NOT supported on /chats/{id}/messages.
				// The API will return all fields; we filter client-side.
			},
		}
		if req.AttachmentExtractor != nil {
			cfg.QueryParameters.Expand = []string{"attachments"}
		}

		result, err := c.graphClient.Chats().ByChatId(req.ChatID).Messages().Get(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to get chat messages: %w", err)
		}

		for _, msg := range result.GetValue() {
			// Skip non-message types (e.g. system events)
			if mt := msg.GetMessageType(); mt != nil && mt.String() != "message" {
				continue
			}

			m := &ChatMessage{}
			if v := msg.GetId(); v != nil {
				m.ID = *v
			}
			if v := msg.GetCreatedDateTime(); v != nil {
				m.CreatedAt = *v
			}
			if from := msg.GetFrom(); from != nil {
				if user := from.GetUser(); user != nil {
					if name := user.GetDisplayName(); name != nil {
						m.From = *name
					}
				}
			}
			if body := msg.GetBody(); body != nil {
				if content := body.GetContent(); content != nil {
					m.Body = *content
				}
			}
			if req.AttachmentExtractor != nil {
				m.Attachments = c.parseChatAttachments(ctx, msg.GetAttachments(), req.AttachmentExtractor)
			}
			results = append(results, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Str("chat_id", req.ChatID).
		Int("count", len(results)).
		Msg("Fetched chat messages successfully")

	return results, nil
}

// parseChatAttachments converts Graph chatMessage attachments to the shared
// EmailDetailsAttachment shape, routing the content through the attachment
// extractor so text formats become truncated markdown and binary/oversize/
// unsupported files are surfaced with a note.
//
// Unlike email attachments (which expose raw bytes via GetContentBytes and a
// Size field), chat attachments carry base64 in Content and have no Size
// field, so we decode and compute the size locally.
func (c *Client) parseChatAttachments(ctx context.Context, atts []models.ChatMessageAttachmentable, extractor *attachments.Extractor) []*EmailDetailsAttachment {
	if atts == nil {
		return nil
	}

	var result []*EmailDetailsAttachment
	for _, att := range atts {
		if att == nil {
			continue
		}
		info := &EmailDetailsAttachment{
			ID:   ptrString(att.GetId()),
			Name: ptrString(att.GetName()),
			Type: "chatAttachment",
		}
		if ct := att.GetContentType(); ct != nil {
			info.ContentType = *ct
		}
		if url := att.GetContentUrl(); url != nil {
			info.WebUrl = *url
		}

		var data []byte
		if content := att.GetContent(); content != nil && *content != "" {
			decoded, decErr := base64.StdEncoding.DecodeString(*content)
			if decErr == nil {
				data = decoded
			} else {
				info.Note = "attachment content is not valid base64; use web_url to fetch"
			}
		}
		info.Size = int64(len(data))

		start := time.Now()
		md, note, kind := extractor.Extract(ctx, data, info.Name, info.ContentType)
		c.recordExtractionMetric(kind, note, time.Since(start))
		info.ContentMarkdown = md
		if info.Note == "" && note != "" {
			info.Note = note
		}

		result = append(result, info)
	}

	return result
}

// SendTeamsMessage sends a message to a specific Teams chat.
// Requires ChatMessage.Send permission.
func (c *Client) SendTeamsMessage(ctx context.Context, chatID string, content string) error {
	err := c.executeWithResilience(ctx, "send_teams_message", func() error {
		message := models.NewChatMessage()

		body := models.NewItemBody()
		bodyContent := content
		body.SetContent(&bodyContent)

		contentType := models.TEXT_BODYTYPE
		body.SetContentType(&contentType)

		message.SetBody(body)

		_, fetchErr := c.graphClient.Chats().ByChatId(chatID).Messages().Post(ctx, message, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to send teams message: %w", fetchErr)
		}

		return nil
	})

	if err == nil {
		c.logger.Info().Str("chat_id", chatID).Msg("Sent Teams message successfully")
	}

	return err
}
