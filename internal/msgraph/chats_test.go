package msgraph

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

func TestParseChatAttachmentsText(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	payload := []byte("hello chat attachment world")
	att := models.NewChatMessageAttachment()
	id := "chat-att-1"
	att.SetId(&id)
	name := "notes.txt"
	att.SetName(&name)
	ct := "text/plain"
	att.SetContentType(&ct)
	encoded := base64.StdEncoding.EncodeToString(payload)
	att.SetContent(&encoded)

	got := c.parseChatAttachments(context.Background(), []models.ChatMessageAttachmentable{att}, extractor)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	info := got[0]
	if info.Type != "chatAttachment" {
		t.Errorf("Type = %q, want chatAttachment", info.Type)
	}
	if info.ContentMarkdown != string(payload) {
		t.Errorf("ContentMarkdown = %q, want %q", info.ContentMarkdown, payload)
	}
	if info.Note != "" {
		t.Errorf("Note = %q, want empty", info.Note)
	}
	if info.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", info.Size, len(payload))
	}
	if info.ContentBase64 != "" {
		t.Error("ContentBase64 should never be populated by the extractor path")
	}
}

func TestParseChatAttachmentsBinary(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	pngHeader := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	att := models.NewChatMessageAttachment()
	id := "chat-att-png"
	att.SetId(&id)
	name := "image.png"
	att.SetName(&name)
	ct := "image/png"
	att.SetContentType(&ct)
	encoded := base64.StdEncoding.EncodeToString(pngHeader)
	att.SetContent(&encoded)

	got := c.parseChatAttachments(context.Background(), []models.ChatMessageAttachmentable{att}, extractor)
	info := got[0]
	if info.ContentMarkdown != "" {
		t.Errorf("ContentMarkdown should be empty for binary, got %q", info.ContentMarkdown)
	}
	if !strings.Contains(info.Note, "binary attachment omitted") {
		t.Errorf("Note %q should mention binary omission", info.Note)
	}
}

func TestParseChatAttachmentsInvalidBase64(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	att := models.NewChatMessageAttachment()
	id := "chat-att-bad"
	att.SetId(&id)
	name := "broken.bin"
	att.SetName(&name)
	notBase64 := "!!!not valid base64!!!"
	att.SetContent(&notBase64)

	got := c.parseChatAttachments(context.Background(), []models.ChatMessageAttachmentable{att}, extractor)
	info := got[0]
	if info.Size != 0 {
		t.Errorf("Size = %d, want 0 when decode fails", info.Size)
	}
	if !strings.Contains(info.Note, "not valid base64") {
		t.Errorf("Note %q should mention base64 failure", info.Note)
	}
}

func TestParseChatAttachmentsNil(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()
	if got := c.parseChatAttachments(context.Background(), nil, extractor); got != nil {
		t.Errorf("got %v, want nil for nil input", got)
	}
}

func TestParseChatAttachmentsContentURL(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	att := models.NewChatMessageAttachment()
	id := "chat-att-url"
	att.SetId(&id)
	name := "card.txt"
	att.SetName(&name)
	url := "https://graph.microsoft.com/hosted/abc"
	att.SetContentUrl(&url)

	got := c.parseChatAttachments(context.Background(), []models.ChatMessageAttachmentable{att}, extractor)
	info := got[0]
	if info.WebUrl != url {
		t.Errorf("WebUrl = %q, want %q", info.WebUrl, url)
	}
	if info.ContentMarkdown != "" {
		t.Errorf("ContentMarkdown should be empty when no content, got %q", info.ContentMarkdown)
	}
}
