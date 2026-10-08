package msgraph

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	abs "github.com/microsoft/kiota-abstractions-go"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// Transcript represents a Teams meeting transcript
type Transcript struct {
	MeetingID      string    `json:"meetingId"`
	TranscriptID   string    `json:"transcriptId"`
	MeetingSubject string    `json:"meetingSubject"`
	MeetingStart   time.Time `json:"meetingStart"`
	CreatedAt      time.Time `json:"createdAt"`
	Content        string    `json:"content"` // VTT format
}

// TranscriptOptions defines how to locate a meeting for transcript retrieval
type TranscriptOptions struct {
	Subject     string     // Partial match on meeting title (contains)
	Date        *time.Time // Specific day to filter to
	LastMeeting bool       // If true, return the most recent Teams meeting with a transcript
	Top         int32      // Max events to try (default 1, max 5)
}

// GetMeetingTranscript finds a Teams meeting and returns its transcript.
// Step A: locate the event via /me/events
// Step B: resolve the onlineMeeting ID via joinWebUrl filter
// Step C: list transcripts for that meeting
// Step D: fetch the VTT content of the first transcript
func (c *Client) GetMeetingTranscript(ctx context.Context, opts TranscriptOptions) (*Transcript, error) {
	if opts.Top == 0 {
		opts.Top = 1
	}

	// ── Step A: find candidate calendar events ──────────────────────────────

	filter, err := buildTranscriptEventFilter(opts)
	if err != nil {
		return nil, err
	}

	type candidateEvent struct {
		joinURL        string
		meetingSubject string
		meetingStart   time.Time
		isOrganizer    bool
	}

	var candidates []candidateEvent

	err = c.executeWithResilience(ctx, "transcript_find_event", func() error {
		top := opts.Top
		if top < 1 {
			top = 1
		}

		// Fetch more events since we cannot filter by isOnlineMeeting via the API
		fetchTop := top
		if fetchTop < 20 {
			fetchTop = 20
		}

		query := &users.ItemEventsRequestBuilderGetQueryParameters{
			Orderby: []string{"start/dateTime desc"},
			Select:  []string{"subject", "start", "onlineMeeting", "isOrganizer"},
			Top:     &fetchTop,
		}
		if filter != "" {
			query.Filter = &filter
		}

		cfg := &users.ItemEventsRequestBuilderGetRequestConfiguration{
			QueryParameters: query,
		}

		result, fetchErr := c.graphClient.Me().Events().Get(ctx, cfg)
		if fetchErr != nil {
			return fmt.Errorf("failed to search events: %w", fetchErr)
		}

		events := result.GetValue()
		if len(events) == 0 {
			return fmt.Errorf("no Teams meeting found matching the given criteria")
		}

		for _, event := range events {
			if event.GetOnlineMeeting() == nil || event.GetOnlineMeeting().GetJoinUrl() == nil {
				continue
			}
			c := candidateEvent{joinURL: *event.GetOnlineMeeting().GetJoinUrl()}
			if subj := event.GetSubject(); subj != nil {
				c.meetingSubject = *subj
			}
			if org := event.GetIsOrganizer(); org != nil {
				c.isOrganizer = *org
			} else {
				c.isOrganizer = false
			}
			if start := event.GetStart(); start != nil {
				if dt := start.GetDateTime(); dt != nil {
					if t, pErr := time.Parse("2006-01-02T15:04:05.0000000", *dt); pErr == nil {
						c.meetingStart = t
					} else if t, pErr := time.Parse(time.RFC3339, *dt); pErr == nil {
						c.meetingStart = t
					}
				}
			}
			candidates = append(candidates, c)
			if int32(len(candidates)) >= top {
				break
			}
		}
		if len(candidates) == 0 {
			return fmt.Errorf("events found but none are Teams online meetings")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Int("candidate_count", len(candidates)).
		Str("first_subject", candidates[0].meetingSubject).
		Msg("Found candidate Teams meeting events")

	// ── Steps B–D: try each candidate until we get a transcript ──────────────

	var lastErr error
	for i, candidate := range candidates {
		c.logger.Debug().
			Int("attempt", i+1).
			Str("subject_digest", observability.Redact(candidate.meetingSubject)).
			Str("join_url", candidate.joinURL).
			Bool("is_organizer", candidate.isOrganizer).
			Msg("Trying candidate meeting")

		if !candidate.isOrganizer {
			lastErr = fmt.Errorf("meeting %q: you are not the meeting organizer. Microsoft Graph API only allows the meeting organizer to retrieve transcripts via Delegated permissions", candidate.meetingSubject)
			c.logger.Warn().Str("subject_digest", observability.Redact(candidate.meetingSubject)).Msg("Skipping candidate: user is not the meeting organizer")
			continue
		}

		// ── Step B: resolve onlineMeeting ID via joinWebUrl ─────────────────
		var meetingID string

		err = c.executeWithResilience(ctx, "transcript_resolve_meeting", func() error {
			// Escape single quotes in the join URL for OData filter safety
			safeURL := strings.ReplaceAll(candidate.joinURL, "'", "''")
			meetingFilter := fmt.Sprintf("joinWebUrl eq '%s'", safeURL)
			cfg := &users.ItemOnlineMeetingsRequestBuilderGetRequestConfiguration{
				QueryParameters: &users.ItemOnlineMeetingsRequestBuilderGetQueryParameters{
					Filter: &meetingFilter,
				},
			}

			result, fetchErr := c.graphClient.Me().OnlineMeetings().Get(ctx, cfg)
			if fetchErr != nil {
				return fmt.Errorf("failed to resolve online meeting: %w", fetchErr)
			}

			meetings := result.GetValue()
			if len(meetings) == 0 {
				return fmt.Errorf("online meeting not found for join URL")
			}

			if id := meetings[0].GetId(); id != nil {
				meetingID = *id
			}
			return nil
		})
		if err != nil {
			lastErr = fmt.Errorf("meeting %q: %w", candidate.meetingSubject, err)
			continue
		}

		c.logger.Debug().
			Str("meeting_id", meetingID).
			Msg("Resolved online meeting ID")

		// ── Step C: list transcripts ────────────────────────────────────────
		var transcriptID string
		var createdAt time.Time

		err = c.executeWithResilience(ctx, "transcript_list", func() error {
			result, fetchErr := c.graphClient.Me().OnlineMeetings().
				ByOnlineMeetingId(meetingID).
				Transcripts().
				Get(ctx, nil)
			if fetchErr != nil {
				return fmt.Errorf("failed to list transcripts: %w", fetchErr)
			}

			transcripts := result.GetValue()
			if len(transcripts) == 0 {
				return fmt.Errorf("no transcripts found for this meeting (transcription may not have been enabled)")
			}

			t := transcripts[0]
			if id := t.GetId(); id != nil {
				transcriptID = *id
			}
			if created := t.GetCreatedDateTime(); created != nil {
				createdAt = *created
			}
			return nil
		})
		if err != nil {
			lastErr = fmt.Errorf("meeting %q: %w", candidate.meetingSubject, err)
			continue
		}

		c.logger.Debug().
			Str("transcript_id", transcriptID).
			Msg("Found transcript")

		// ── Step D: fetch transcript content (VTT) ──────────────────────────
		var content []byte

		err = c.executeWithResilience(ctx, "transcript_content", func() error {
			// Set Accept header to text/vtt to get VTT-formatted transcript text.
			// Without this, the SDK defaults to application/octet-stream which
			// can cause the API to fail or return unusable binary content.
			vttHeaders := abs.NewRequestHeaders()
			vttHeaders.Add("Accept", "text/vtt")
			contentCfg := &users.ItemOnlineMeetingsItemTranscriptsItemContentRequestBuilderGetRequestConfiguration{
				Headers: vttHeaders,
			}

			var fetchErr error
			content, fetchErr = c.graphClient.Me().OnlineMeetings().
				ByOnlineMeetingId(meetingID).
				Transcripts().
				ByCallTranscriptId(transcriptID).
				Content().
				Get(ctx, contentCfg)
			if fetchErr != nil {
				return fmt.Errorf("failed to fetch transcript content: %w", fetchErr)
			}
			return nil
		})
		if err != nil {
			lastErr = fmt.Errorf("meeting %q: %w", candidate.meetingSubject, err)
			continue
		}

		c.logger.Debug().
			Int("content_bytes", len(content)).
			Str("meeting_subject", candidate.meetingSubject).
			Msg("Fetched transcript content successfully")

		return &Transcript{
			MeetingID:      meetingID,
			TranscriptID:   transcriptID,
			MeetingSubject: candidate.meetingSubject,
			MeetingStart:   candidate.meetingStart,
			CreatedAt:      createdAt,
			Content:        string(content),
		}, nil
	}

	// All candidates exhausted
	if lastErr != nil {
		return nil, fmt.Errorf("could not retrieve transcript from any of the %d matching meetings: %w", len(candidates), lastErr)
	}
	return nil, fmt.Errorf("no transcript found")
}

// buildTranscriptEventFilter constructs the OData filter for the event search.
func buildTranscriptEventFilter(opts TranscriptOptions) (string, error) {
	var parts []string

	if opts.Subject != "" {
		// Escape single quotes in subject
		safe := strings.ReplaceAll(opts.Subject, "'", "''")
		parts = append(parts, fmt.Sprintf("contains(subject, '%s')", safe))
	}

	if opts.Date != nil {
		dayStart := opts.Date.Truncate(24 * time.Hour).UTC()
		dayEnd := dayStart.Add(24 * time.Hour)
		parts = append(parts,
			fmt.Sprintf("start/dateTime ge '%s'", dayStart.Format("2006-01-02T15:04:05Z")),
			fmt.Sprintf("start/dateTime lt '%s'", dayEnd.Format("2006-01-02T15:04:05Z")),
		)
	}

	if opts.LastMeeting {
		now := time.Now().UTC()
		parts = append(parts, fmt.Sprintf("start/dateTime le '%s'", now.Format("2006-01-02T15:04:05Z")))
	}

	if len(parts) == 0 {
		return "", nil
	}

	return strings.Join(parts, " and "), nil
}
