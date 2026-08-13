package msgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

func TestExtractRecipientAddresses(t *testing.T) {
	tests := []struct {
		name       string
		recipients []models.Recipientable
		want       []string
	}{
		{
			name:       "nil recipients",
			recipients: nil,
			want:       nil,
		},
		{
			name: "single recipient",
			recipients: []models.Recipientable{
				newRecipient("alice@example.com"),
			},
			want: []string{"alice@example.com"},
		},
		{
			name: "multiple recipients",
			recipients: []models.Recipientable{
				newRecipient("alice@example.com"),
				newRecipient("bob@example.com"),
			},
			want: []string{"alice@example.com", "bob@example.com"},
		},
		{
			name: "skips nil recipients",
			recipients: []models.Recipientable{
				newRecipient("alice@example.com"),
				nil,
				newRecipient("charlie@example.com"),
			},
			want: []string{"alice@example.com", "charlie@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRecipientAddresses(tt.recipients)
			if len(got) != len(tt.want) {
				t.Errorf("extractRecipientAddresses() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractRecipientAddresses()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPtrString(t *testing.T) {
	s := "hello"
	if got := ptrString(&s); got != "hello" {
		t.Errorf("ptrString(&s) = %q, want %q", got, "hello")
	}
	if got := ptrString(nil); got != "" {
		t.Errorf("ptrString(nil) = %q, want empty string", got)
	}
}

func TestExtractStringFromAttachmentAdditionalData(t *testing.T) {
	att := models.NewReferenceAttachment()
	att.SetAdditionalData(map[string]interface{}{
		"sourceUrl": "https://example.com/file.docx",
		"webUrl":    "https://example.com/web",
	})

	if got := extractStringFromAttachmentAdditionalData(att, "sourceUrl"); got != "https://example.com/file.docx" {
		t.Errorf("extractStringFromAttachmentAdditionalData(sourceUrl) = %q, want %q", got, "https://example.com/file.docx")
	}
	if got := extractStringFromAttachmentAdditionalData(att, "missing"); got != "" {
		t.Errorf("extractStringFromAttachmentAdditionalData(missing) = %q, want empty string", got)
	}
	if got := extractStringFromAttachmentAdditionalData(nil, "sourceUrl"); got != "" {
		t.Errorf("extractStringFromAttachmentAdditionalData(nil) = %q, want empty string", got)
	}
}

func TestConvertToEmailDetails(t *testing.T) {
	c := &Client{}

	msg := models.NewMessage()
	id := "msg-123"
	msg.SetId(&id)
	subject := "Test Subject"
	msg.SetSubject(&subject)
	from := models.NewRecipient()
	fromEmail := models.NewEmailAddress()
	fromAddr := "sender@example.com"
	fromEmail.SetAddress(&fromAddr)
	from.SetEmailAddress(fromEmail)
	msg.SetFrom(from)

	to1 := newRecipient("to1@example.com")
	to2 := newRecipient("to2@example.com")
	msg.SetToRecipients([]models.Recipientable{to1, to2})

	received := time.Date(2026, 7, 3, 10, 30, 0, 0, time.UTC)
	msg.SetReceivedDateTime(&received)

	hasAttachments := true
	msg.SetHasAttachments(&hasAttachments)

	body := models.NewItemBody()
	bodyContent := "<html><body>Hello</body></html>"
	body.SetContent(&bodyContent)
	bodyType := models.HTML_BODYTYPE
	body.SetContentType(&bodyType)
	msg.SetBody(body)

	fileAtt := models.NewFileAttachment()
	attID := "att-1"
	fileAtt.SetId(&attID)
	attName := "report.pdf"
	fileAtt.SetName(&attName)
	attContentType := "application/pdf"
	fileAtt.SetContentType(&attContentType)
	attSize := int32(1024)
	fileAtt.SetSize(&attSize)
	fileAtt.SetContentBytes([]byte("pdf-content"))

	msg.SetAttachments([]models.Attachmentable{fileAtt})

	details := c.convertToEmailDetails(context.Background(), msg, true, defaultInlineAttachmentLimit, 0, nil)
	if details == nil {
		t.Fatal("convertToEmailDetails returned nil")
	}

	if details.ID != "msg-123" {
		t.Errorf("ID = %q, want msg-123", details.ID)
	}
	if details.Subject != "Test Subject" {
		t.Errorf("Subject = %q, want Test Subject", details.Subject)
	}
	if details.From != "sender@example.com" {
		t.Errorf("From = %q, want sender@example.com", details.From)
	}
	if len(details.To) != 2 || details.To[0] != "to1@example.com" || details.To[1] != "to2@example.com" {
		t.Errorf("To = %v, want [to1@example.com to2@example.com]", details.To)
	}
	if details.BodyType != "html" {
		t.Errorf("BodyType = %q, want html", details.BodyType)
	}
	if !details.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
	if len(details.Attachments) != 1 {
		t.Fatalf("Attachments count = %d, want 1", len(details.Attachments))
	}

	att := details.Attachments[0]
	if att.ID != "att-1" {
		t.Errorf("Attachment.ID = %q, want att-1", att.ID)
	}
	if att.Type != "fileAttachment" {
		t.Errorf("Attachment.Type = %q, want fileAttachment", att.Type)
	}
	if att.ContentBase64 == "" {
		t.Error("Attachment.ContentBase64 is empty, want base64 content")
	}
}

func TestConvertToEmailDetailsLargeAttachment(t *testing.T) {
	c := &Client{}

	msg := models.NewMessage()
	id := "msg-large"
	msg.SetId(&id)

	body := models.NewItemBody()
	bodyContent := "body"
	body.SetContent(&bodyContent)
	bodyType := models.TEXT_BODYTYPE
	body.SetContentType(&bodyType)
	msg.SetBody(body)

	fileAtt := models.NewFileAttachment()
	attID := "att-large"
	fileAtt.SetId(&attID)
	attName := "big.bin"
	fileAtt.SetName(&attName)
	// Actual returned bytes exceed the inline limit.
	largeContent := make([]byte, defaultInlineAttachmentLimit+1)
	fileAtt.SetContentBytes(largeContent)
	msg.SetAttachments([]models.Attachmentable{fileAtt})

	details := c.convertToEmailDetails(context.Background(), msg, true, defaultInlineAttachmentLimit, 0, nil)
	att := details.Attachments[0]
	if att.ContentBase64 != "" {
		t.Error("Expected large attachment to omit base64 content")
	}
	if att.Note == "" {
		t.Error("Expected note about size limit")
	}
}

func TestConvertToEmailDetailsWithExtractorText(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	msg := models.NewMessage()
	id := "msg-ext"
	msg.SetId(&id)

	fileAtt := models.NewFileAttachment()
	attID := "att-txt"
	fileAtt.SetId(&attID)
	attName := "notes.txt"
	fileAtt.SetName(&attName)
	attContentType := "text/plain"
	fileAtt.SetContentType(&attContentType)
	fileAtt.SetSize(int32Ptr(int32(len("hello from the attachment"))))
	fileAtt.SetContentBytes([]byte("hello from the attachment"))
	msg.SetAttachments([]models.Attachmentable{fileAtt})

	details := c.convertToEmailDetails(context.Background(), msg, true, defaultInlineAttachmentLimit, 0, extractor)
	if len(details.Attachments) != 1 {
		t.Fatalf("Attachments count = %d, want 1", len(details.Attachments))
	}
	att := details.Attachments[0]
	if att.ContentBase64 != "" {
		t.Error("ContentBase64 should be empty when extractor is wired")
	}
	if att.ContentMarkdown != "hello from the attachment" {
		t.Errorf("ContentMarkdown = %q, want extracted content", att.ContentMarkdown)
	}
	if att.Note != "" {
		t.Errorf("Note = %q, want empty", att.Note)
	}
}

func TestConvertToEmailDetailsWithExtractorBinary(t *testing.T) {
	c := &Client{}
	extractor := attachments.New()

	msg := models.NewMessage()
	id := "msg-bin"
	msg.SetId(&id)

	fileAtt := models.NewFileAttachment()
	attID := "att-png"
	fileAtt.SetId(&attID)
	attName := "logo.png"
	fileAtt.SetName(&attName)
	attContentType := "image/png"
	fileAtt.SetContentType(&attContentType)
	fileAtt.SetContentBytes([]byte{0x89, 0x50, 0x4e, 0x47})
	msg.SetAttachments([]models.Attachmentable{fileAtt})

	details := c.convertToEmailDetails(context.Background(), msg, true, defaultInlineAttachmentLimit, 0, extractor)
	att := details.Attachments[0]
	if att.ContentBase64 != "" {
		t.Error("ContentBase64 should be empty for binary attachment with extractor wired")
	}
	if att.ContentMarkdown != "" {
		t.Errorf("ContentMarkdown should be empty for binary, got %q", att.ContentMarkdown)
	}
	if !strings.Contains(att.Note, "binary attachment omitted") {
		t.Errorf("Note %q should mention binary omission", att.Note)
	}
}

func TestTruncateBody(t *testing.T) {
	t.Run("disabled when max is zero", func(t *testing.T) {
		if got := truncateBody("hello", 0); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("disabled when under cap", func(t *testing.T) {
		if got := truncateBody("hello", 100); got != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	})

	t.Run("truncated at cap with marker", func(t *testing.T) {
		body := strings.Repeat("a", 200)
		got := truncateBody(body, 50)
		if !strings.Contains(got, "[body truncated at 50 bytes") {
			t.Errorf("expected truncation marker, got %q", got)
		}
		if !strings.Contains(got, "original was 200 bytes") {
			t.Errorf("expected original length in marker, got %q", got)
		}
	})
}

func newRecipient(addr string) models.Recipientable {
	r := models.NewRecipient()
	email := models.NewEmailAddress()
	email.SetAddress(&addr)
	r.SetEmailAddress(email)
	return r
}

func int32Ptr(n int32) *int32 { return &n }
