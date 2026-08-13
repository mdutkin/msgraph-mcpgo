package gemini

import (
	"context"
	"fmt"

	"github.com/fnfbraga/msgraph-mcpgo/internal/msgraph"
)

// ActivityData holds user activity data for summarization
type ActivityData struct {
	Emails    []*msgraph.Email
	Files     []*msgraph.File
	Events    []*msgraph.Event
	TimeRange string
}

// Summary represents the AI-generated summary
type Summary struct {
	Text       string         `json:"summary"`
	ItemCounts map[string]int `json:"itemCount"`
}

// EmailAnalysis represents analysis of emails
type EmailAnalysis struct {
	UrgentMessages []string          `json:"urgentMessages"`
	ActionItems    []string          `json:"actionItems"`
	Summary        string            `json:"summary"`
	Patterns       map[string]string `json:"patterns"`
}

// SummarizeActivity generates a summary of user activity
func (a *Agent) SummarizeActivity(ctx context.Context, data *ActivityData) (*Summary, error) {
	// Prepare prompt data
	promptData := PromptData{
		TimeRange:  data.TimeRange,
		Emails:     data.Emails,
		Files:      data.Files,
		Events:     data.Events,
		EmailCount: len(data.Emails),
		FileCount:  len(data.Files),
		EventCount: len(data.Events),
	}

	// Render the prompt
	prompt, err := RenderTemplate(SummaryPromptTemplate, promptData)
	if err != nil {
		return nil, fmt.Errorf("failed to render prompt: %w", err)
	}

	a.logger.Debug().
		Int("email_count", len(data.Emails)).
		Int("file_count", len(data.Files)).
		Int("event_count", len(data.Events)).
		Str("time_range", data.TimeRange).
		Msg("Generating activity summary")

	// Generate content
	summaryText, err := a.GenerateContent(ctx, prompt)
	if err != nil {
		return nil, err
	}

	// Create summary response
	summary := &Summary{
		Text: summaryText,
		ItemCounts: map[string]int{
			"emails": len(data.Emails),
			"files":  len(data.Files),
			"events": len(data.Events),
		},
	}

	return summary, nil
}

// AnalyzeEmails analyzes a list of emails for insights
func (a *Agent) AnalyzeEmails(ctx context.Context, emails []*msgraph.Email) (*EmailAnalysis, error) {
	if len(emails) == 0 {
		return &EmailAnalysis{
			UrgentMessages: []string{},
			ActionItems:    []string{},
			Summary:        "No emails to analyze.",
			Patterns:       map[string]string{},
		}, nil
	}

	// Prepare prompt data
	promptData := struct {
		Emails     []*msgraph.Email
		EmailCount int
	}{
		Emails:     emails,
		EmailCount: len(emails),
	}

	// Render the prompt
	prompt, err := RenderTemplate(EmailAnalysisPromptTemplate, promptData)
	if err != nil {
		return nil, fmt.Errorf("failed to render email analysis prompt: %w", err)
	}

	a.logger.Debug().
		Int("email_count", len(emails)).
		Msg("Analyzing emails")

	// Generate analysis
	analysisText, err := a.GenerateContent(ctx, prompt)
	if err != nil {
		return nil, err
	}

	// For now, return the analysis as a simple summary
	// In a more sophisticated implementation, we could parse the structured output
	analysis := &EmailAnalysis{
		Summary:        analysisText,
		UrgentMessages: []string{},
		ActionItems:    []string{},
		Patterns:       map[string]string{},
	}

	return analysis, nil
}

// SummarizeChat generates a summary of a Teams chat
func (a *Agent) SummarizeChat(ctx context.Context, messages []*msgraph.ChatMessage) (*Summary, error) {
	if len(messages) == 0 {
		return &Summary{
			Text: "No messages to summarize.",
		}, nil
	}

	// Prepare prompt data
	promptData := PromptData{
		Messages: messages,
	}

	// Render the prompt
	prompt, err := RenderTemplate(ChatSummaryPromptTemplate, promptData)
	if err != nil {
		return nil, fmt.Errorf("failed to render chat summary prompt: %w", err)
	}

	a.logger.Debug().
		Int("message_count", len(messages)).
		Msg("Generating chat summary")

	// Generate content
	summaryText, err := a.GenerateContent(ctx, prompt)
	if err != nil {
		return nil, err
	}

	// Create summary response
	summary := &Summary{
		Text: summaryText,
	}

	return summary, nil
}
