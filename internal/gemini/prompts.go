package gemini

import (
	"bytes"
	"text/template"
)

// SummaryPromptTemplate is the template for activity summary
const SummaryPromptTemplate = `You are an assistant summarizing a user's recent Microsoft 365 activity.

Time Range: {{.TimeRange}}

{{if gt .EmailCount 0}}
Recent Emails ({{.EmailCount}}):
{{range .Emails}}
- From: {{.From}}
  Subject: {{.Subject}}
  Received: {{.ReceivedTime.Format "Jan 2, 3:04 PM"}}
  Preview: {{.BodyPreview}}
{{end}}
{{else}}
No recent emails.
{{end}}

{{if gt .FileCount 0}}
Recent Files ({{.FileCount}}):
{{range .Files}}
- Name: {{.Name}}
  Modified: {{.ModifiedTime.Format "Jan 2, 3:04 PM"}}
  {{if .Path}}Path: {{.Path}}{{end}}
{{end}}
{{else}}
No recent file activity.
{{end}}

{{if gt .EventCount 0}}
Upcoming Calendar Events ({{.EventCount}}):
{{range .Events}}
- Subject: {{.Subject}}
  Time: {{.Start.Format "Jan 2, 3:04 PM"}} to {{.End.Format "3:04 PM"}}
  {{if .Location}}Location: {{.Location}}{{end}}
  {{if .IsOnline}}(Online Meeting){{end}}
{{end}}
{{else}}
No upcoming calendar events.
{{end}}

Please provide a concise summary (3-5 sentences) highlighting:
1. Important emails or messages requiring attention
2. Recent file activity and collaborations
3. Upcoming meetings and commitments
4. Any action items or priorities

Keep the summary brief, actionable, and focused on what matters most to the user.`

// EmailAnalysisPromptTemplate is the template for email analysis
const EmailAnalysisPromptTemplate = `Analyze the following emails and identify:
1. Urgent or important messages
2. Action items or requests
3. Decisions needed
4. Any patterns or trends

Emails:
{{range .Emails}}
---
From: {{.From}}
Subject: {{.Subject}}
Received: {{.ReceivedTime.Format "Jan 2, 3:04 PM"}}
Preview: {{.BodyPreview}}
---
{{end}}

Provide a structured analysis with actionable insights.`

// ChatSummaryPromptTemplate is the template for chat history summary
const ChatSummaryPromptTemplate = `Analyze the following Teams chat history and provide a concise summary.
Highlight any pending decisions, action items, or important information shared.

Chat Messages:
{{range .Messages}}
---
From: {{.From}}
Time: {{if not .CreatedAt.IsZero}}{{.CreatedAt.Format "Jan 2, 3:04 PM"}}{{else}}unknown{{end}}
Message: {{.Body}}
---
{{end}}

Please provide a 3-5 sentence summary and a bulleted list of any action items.`

// PromptData holds data for template rendering
type PromptData struct {
	TimeRange  string
	Emails     interface{}
	Files      interface{}
	Events     interface{}
	Messages   interface{}
	EmailCount int
	FileCount  int
	EventCount int
}

// RenderTemplate renders a prompt template with data
func RenderTemplate(templateStr string, data interface{}) (string, error) {
	tmpl, err := template.New("prompt").Parse(templateStr)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
