package msgraph

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	abs "github.com/microsoft/kiota-abstractions-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// Event represents a calendar event
type Event struct {
	ID            string    `json:"id"`
	Subject       string    `json:"subject"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	StartTimeZone string    `json:"startTimeZone,omitempty"`
	EndTimeZone   string    `json:"endTimeZone,omitempty"`
	Location      string    `json:"location"`
	Attendees     []string  `json:"attendees"`
	IsOnline      bool      `json:"isOnline"`
	WebLink       string    `json:"webLink"`
}

// defaultTimezone is used when no timezone is provided by the caller.
// Europe/Zurich provides a stable default when the caller omits a timezone.
const defaultTimezone = "Europe/Zurich"

// GetCalendarEvents fetches calendar events in a time range.
// The timezone parameter (IANA, e.g. "Europe/Zurich") tells the Graph API to
// return event times localised to that timezone via the Prefer header.
func (c *Client) GetCalendarEvents(ctx context.Context, start, end time.Time, timezone string) ([]*Event, error) {
	if timezone == "" {
		timezone = defaultTimezone
	}

	var graphEvents []models.Eventable
	var err error

	err = c.executeWithResilience(ctx, "calendar_events", func() error {
		// CalendarView requires startDateTime and endDateTime as query parameters
		startStr := start.Format(time.RFC3339)
		endStr := end.Format(time.RFC3339)

		// Build request headers — ask Graph to return times in the user's tz
		headers := abs.NewRequestHeaders()
		headers.Add("Prefer", fmt.Sprintf(`outlook.timezone="%s"`, timezone))

		// Build request configuration
		requestConfig := &users.ItemCalendarViewRequestBuilderGetRequestConfiguration{
			Headers: headers,
			QueryParameters: &users.ItemCalendarViewRequestBuilderGetQueryParameters{
				StartDateTime: &startStr,
				EndDateTime:   &endStr,
			},
		}

		// Fetch calendar events
		result, fetchErr := c.graphClient.Me().CalendarView().Get(ctx, requestConfig)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch calendar events: %w", fetchErr)
		}

		graphEvents = result.GetValue()
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Convert to our Event type
	events := make([]*Event, 0, len(graphEvents))
	for _, graphEvent := range graphEvents {
		event := c.convertToEvent(graphEvent)
		if event != nil {
			events = append(events, event)
		}
	}

	c.logger.Debug().
		Int("count", len(events)).
		Time("start", start).
		Time("end", end).
		Msg("Fetched calendar events successfully")

	return events, nil
}

// GetUpcomingEvents fetches upcoming calendar events for the next N days
func (c *Client) GetUpcomingEvents(ctx context.Context, days int, top int32, timezone string) ([]*Event, error) {
	if timezone == "" {
		timezone = defaultTimezone
	}

	start := time.Now()
	end := start.AddDate(0, 0, days)

	var graphEvents []models.Eventable
	var err error

	err = c.executeWithResilience(ctx, "calendar_upcoming", func() error {
		// CalendarView requires startDateTime and endDateTime as query parameters
		startStr := start.Format(time.RFC3339)
		endStr := end.Format(time.RFC3339)

		// Build request headers — ask Graph to return times in the user's tz
		headers := abs.NewRequestHeaders()
		headers.Add("Prefer", fmt.Sprintf(`outlook.timezone="%s"`, timezone))

		// Build request configuration
		requestConfig := &users.ItemCalendarViewRequestBuilderGetRequestConfiguration{
			Headers: headers,
			QueryParameters: &users.ItemCalendarViewRequestBuilderGetQueryParameters{
				StartDateTime: &startStr,
				EndDateTime:   &endStr,
				Top:           &top,
			},
		}

		// Fetch calendar events
		result, fetchErr := c.graphClient.Me().CalendarView().Get(ctx, requestConfig)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch upcoming events: %w", fetchErr)
		}

		graphEvents = result.GetValue()
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Convert to our Event type
	events := make([]*Event, 0, len(graphEvents))
	for _, graphEvent := range graphEvents {
		event := c.convertToEvent(graphEvent)
		if event != nil && event.Start.Before(end) {
			events = append(events, event)
		}
	}

	c.logger.Debug().
		Int("count", len(events)).
		Int("days", days).
		Msg("Fetched upcoming events successfully")

	return events, nil
}

// graphDateTimeFormats lists the datetime formats returned by the Microsoft
// Graph API, from most specific to least.  The Graph SDK typically returns
// "2006-01-02T15:04:05.0000000" (7-digit fractional seconds, no tz suffix).
var graphDateTimeFormats = []string{
	time.RFC3339Nano,              // 2006-01-02T15:04:05.999999999Z07:00
	time.RFC3339,                  // 2006-01-02T15:04:05Z07:00
	"2006-01-02T15:04:05.0000000", // Graph default (7-digit frac, no tz)
	"2006-01-02T15:04:05.000000",  // 6-digit
	"2006-01-02T15:04:05.00000",   // 5-digit
	"2006-01-02T15:04:05.0000",    // 4-digit
	"2006-01-02T15:04:05.000",     // 3-digit (milliseconds)
	"2006-01-02T15:04:05",         // no fractional seconds
}

// windowsToIANA maps common Windows timezone names (as returned by the MS Graph
// API) to IANA timezone identifiers that Go's time.LoadLocation can resolve.
var windowsToIANA = map[string]string{
	// UTC / GMT
	"UTC":                     "UTC",
	"GMT Standard Time":       "Europe/London",
	"Greenwich Standard Time": "Atlantic/Reykjavik",
	// Western Europe
	"W. Europe Standard Time":        "Europe/Berlin",
	"Central European Standard Time": "Europe/Budapest",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Romance Standard Time":          "Europe/Paris",
	// Eastern Europe
	"E. Europe Standard Time": "Europe/Chisinau",
	"FLE Standard Time":       "Europe/Kiev",
	"GTB Standard Time":       "Europe/Bucharest",
	// Russia
	"Russian Standard Time":     "Europe/Moscow",
	"Kaliningrad Standard Time": "Europe/Kaliningrad",
	// US
	"Eastern Standard Time":     "America/New_York",
	"Central Standard Time":     "America/Chicago",
	"Mountain Standard Time":    "America/Denver",
	"Pacific Standard Time":     "America/Los_Angeles",
	"Alaskan Standard Time":     "America/Anchorage",
	"Hawaiian Standard Time":    "Pacific/Honolulu",
	"US Mountain Standard Time": "America/Phoenix",
	"US Eastern Standard Time":  "America/Indianapolis",
	// Canada / Americas
	"Atlantic Standard Time":         "America/Halifax",
	"Newfoundland Standard Time":     "America/St_Johns",
	"SA Pacific Standard Time":       "America/Bogota",
	"SA Eastern Standard Time":       "America/Cayenne",
	"SA Western Standard Time":       "America/La_Paz",
	"E. South America Standard Time": "America/Sao_Paulo",
	"Central America Standard Time":  "America/Guatemala",
	// Asia
	"India Standard Time":     "Asia/Kolkata",
	"China Standard Time":     "Asia/Shanghai",
	"Tokyo Standard Time":     "Asia/Tokyo",
	"Korea Standard Time":     "Asia/Seoul",
	"Singapore Standard Time": "Asia/Singapore",
	"SE Asia Standard Time":   "Asia/Bangkok",
	"Arabian Standard Time":   "Asia/Dubai",
	"Arab Standard Time":      "Asia/Riyadh",
	"Israel Standard Time":    "Asia/Jerusalem",
	"Turkey Standard Time":    "Europe/Istanbul",
	// Australia / Pacific
	"AUS Eastern Standard Time":    "Australia/Sydney",
	"AUS Central Standard Time":    "Australia/Darwin",
	"E. Australia Standard Time":   "Australia/Brisbane",
	"Cen. Australia Standard Time": "Australia/Adelaide",
	"W. Australia Standard Time":   "Australia/Perth",
	"New Zealand Standard Time":    "Pacific/Auckland",
	"Fiji Standard Time":           "Pacific/Fiji",
	// Africa
	"South Africa Standard Time": "Africa/Johannesburg",
	"Egypt Standard Time":        "Africa/Cairo",
	"Morocco Standard Time":      "Africa/Casablanca",
}

// loadTimezone resolves a timezone string from the MS Graph API to a
// *time.Location. It handles IANA names directly, and maps Windows timezone
// names via the windowsToIANA table.
func loadTimezone(tz string) (*time.Location, error) {
	if tz == "" || tz == "UTC" || tz == "tzone://Microsoft/Utc" {
		return time.UTC, nil
	}
	// Try IANA name first (Go natively supports these).
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc, nil
	}
	// Try Windows name mapping.
	if iana, ok := windowsToIANA[tz]; ok {
		return time.LoadLocation(iana)
	}
	return nil, fmt.Errorf("unknown timezone %q", tz)
}

// parseGraphDateTime parses a dateTime string from a Graph DateTimeTimeZone
// object, using the accompanying timeZone value to produce a correct time.Time.
// The returned time preserves the original timezone so consumers see local times
// (e.g. 13:00+01:00 for CET) rather than raw UTC offsets.
func parseGraphDateTime(dateTimeStr, timeZoneStr string) (time.Time, error) {
	// Try each known format until one succeeds.
	var parsed time.Time
	var parseErr error
	for _, layout := range graphDateTimeFormats {
		parsed, parseErr = time.Parse(layout, dateTimeStr)
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return time.Time{}, fmt.Errorf("unable to parse Graph dateTime %q: %w", dateTimeStr, parseErr)
	}

	// If the parsed time already carries a real timezone (RFC3339 with Z or
	// offset), use it directly.
	_, offset := parsed.Zone()
	if parsed.Location() != time.UTC || offset != 0 || strings.HasSuffix(strings.TrimSpace(dateTimeStr), "Z") {
		// The value was already tz-qualified; keep it as-is.
		return parsed, nil
	}

	// Otherwise the dateTime is a "floating" value; locate it in the
	// companion timeZone.
	loc, err := loadTimezone(timeZoneStr)
	if err != nil {
		// Unknown timezone — return as UTC rather than failing.
		return parsed.UTC(), nil
	}

	// Re-interpret the "floating" wall-clock time in the resolved timezone.
	localized := time.Date(parsed.Year(), parsed.Month(), parsed.Day(),
		parsed.Hour(), parsed.Minute(), parsed.Second(),
		parsed.Nanosecond(), loc)
	return localized, nil
}

// convertToEvent converts a Graph SDK event to our Event type
func (c *Client) convertToEvent(graphEvent models.Eventable) *Event {
	if graphEvent == nil {
		return nil
	}

	event := &Event{}

	if id := graphEvent.GetId(); id != nil {
		event.ID = *id
	}

	if subject := graphEvent.GetSubject(); subject != nil {
		event.Subject = *subject
	}

	if start := graphEvent.GetStart(); start != nil {
		if startTime := start.GetDateTime(); startTime != nil {
			tz := ""
			if tzVal := start.GetTimeZone(); tzVal != nil {
				tz = *tzVal
			}
			if parsed, err := parseGraphDateTime(*startTime, tz); err == nil {
				event.Start = parsed
				event.StartTimeZone = tz
			} else {
				c.logger.Warn().Err(err).Str("raw", *startTime).Str("tz", tz).Msg("Failed to parse event start time")
			}
		}
	}

	if end := graphEvent.GetEnd(); end != nil {
		if endTime := end.GetDateTime(); endTime != nil {
			tz := ""
			if tzVal := end.GetTimeZone(); tzVal != nil {
				tz = *tzVal
			}
			if parsed, err := parseGraphDateTime(*endTime, tz); err == nil {
				event.End = parsed
				event.EndTimeZone = tz
			} else {
				c.logger.Warn().Err(err).Str("raw", *endTime).Str("tz", tz).Msg("Failed to parse event end time")
			}
		}
	}

	if location := graphEvent.GetLocation(); location != nil {
		if displayName := location.GetDisplayName(); displayName != nil {
			event.Location = *displayName
		}
	}

	if attendees := graphEvent.GetAttendees(); attendees != nil {
		event.Attendees = make([]string, 0, len(attendees))
		for _, attendee := range attendees {
			if emailAddr := attendee.GetEmailAddress(); emailAddr != nil {
				if addr := emailAddr.GetAddress(); addr != nil {
					event.Attendees = append(event.Attendees, *addr)
				}
			}
		}
	}

	if isOnline := graphEvent.GetIsOnlineMeeting(); isOnline != nil {
		event.IsOnline = *isOnline
	}

	if webLink := graphEvent.GetWebLink(); webLink != nil {
		event.WebLink = *webLink
	}

	return event
}

// ScheduleMeeting creates a new Teams meeting in the user's calendar.
// Requires Calendar.ReadWrite permission.
func (c *Client) ScheduleMeeting(ctx context.Context, subject string, attendees []string, start, end time.Time) (*Event, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}

	var resultEvent *Event

	err := c.executeWithResilience(ctx, "calendar_schedule", func() error {
		event := models.NewEvent()

		// Set subject
		event.SetSubject(&subject)

		// Set start time
		startDt := models.NewDateTimeTimeZone()
		startStr := start.UTC().Format("2006-01-02T15:04:05")
		startDt.SetDateTime(&startStr)
		startTz := "UTC"
		startDt.SetTimeZone(&startTz)
		event.SetStart(startDt)

		// Set end time
		endDt := models.NewDateTimeTimeZone()
		endStr := end.UTC().Format("2006-01-02T15:04:05")
		endDt.SetDateTime(&endStr)
		endTz := "UTC"
		endDt.SetTimeZone(&endTz)
		event.SetEnd(endDt)

		// Set attendees
		if len(attendees) > 0 {
			graphAttendees := make([]models.Attendeeable, 0, len(attendees))
			for _, email := range attendees {
				attendee := models.NewAttendee()
				emailAddress := models.NewEmailAddress()
				addr := email // copy loop var
				emailAddress.SetAddress(&addr)
				attendee.SetEmailAddress(emailAddress)

				attendeeType := models.REQUIRED_ATTENDEETYPE
				attendee.SetTypeEscaped(&attendeeType)

				graphAttendees = append(graphAttendees, attendee)
			}
			event.SetAttendees(graphAttendees)
		}

		// Set as online Teams meeting
		isOnline := true
		provider := models.TEAMSFORBUSINESS_ONLINEMEETINGPROVIDERTYPE
		event.SetIsOnlineMeeting(&isOnline)
		event.SetOnlineMeetingProvider(&provider)

		// Post to /me/events
		reqConfig := &users.ItemEventsRequestBuilderPostRequestConfiguration{}
		createdEvent, fetchErr := c.graphClient.Me().Events().Post(ctx, event, reqConfig)
		if fetchErr != nil {
			return fmt.Errorf("failed to schedule meeting: %w", fetchErr)
		}

		resultEvent = c.convertToEvent(createdEvent)
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Info().
		Str("subject_digest", observability.Redact(subject)).
		Int("attendees", len(attendees)).
		Msg("Scheduled meeting successfully")

	return resultEvent, nil
}

// ScheduleInfo represents the availability information for a user.
// Subject and location are deliberately omitted to protect other users' privacy.
type ScheduleInfo struct {
	Email            string          `json:"email"`
	AvailabilityView string          `json:"availabilityView"`
	ScheduleItems    []*ScheduleSlot `json:"scheduleItems"`
	Error            string          `json:"error,omitempty"`
}

// ScheduleSlot represents a single time slot in a user's schedule.
type ScheduleSlot struct {
	Status string    `json:"status"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
}

// GetSchedule retrieves free/busy availability for the specified users.
// Privacy: only returns status and time windows, NOT subject or location.
// Requires Calendars.Read (delegated) permission.
func (c *Client) GetSchedule(ctx context.Context, emails []string, start, end time.Time, timezone string, intervalMinutes int32) ([]*ScheduleInfo, error) {
	if timezone == "" {
		timezone = defaultTimezone
	}
	if intervalMinutes <= 0 {
		intervalMinutes = 30
	}

	var schedules []*ScheduleInfo

	err := c.executeWithResilience(ctx, "calendar_get_schedule", func() error {
		requestBody := users.NewItemCalendarGetSchedulePostRequestBody()
		requestBody.SetSchedules(emails)

		startDt := models.NewDateTimeTimeZone()
		startStr := start.UTC().Format("2006-01-02T15:04:05")
		startDt.SetDateTime(&startStr)
		utcTz := "UTC"
		startDt.SetTimeZone(&utcTz)
		requestBody.SetStartTime(startDt)

		endDt := models.NewDateTimeTimeZone()
		endStr := end.UTC().Format("2006-01-02T15:04:05")
		endDt.SetDateTime(&endStr)
		endDt.SetTimeZone(&utcTz)
		requestBody.SetEndTime(endDt)

		requestBody.SetAvailabilityViewInterval(&intervalMinutes)

		result, fetchErr := c.graphClient.Me().Calendar().GetSchedule().Post(ctx, requestBody, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to get schedule: %w", fetchErr)
		}

		for _, info := range result.GetValue() {
			si := &ScheduleInfo{}

			if email := info.GetScheduleId(); email != nil {
				si.Email = *email
			}

			if av := info.GetAvailabilityView(); av != nil {
				si.AvailabilityView = *av
			}

			if schedErr := info.GetError(); schedErr != nil {
				if msg := schedErr.GetMessage(); msg != nil {
					si.Error = *msg
				}
			}

			if items := info.GetScheduleItems(); items != nil {
				for _, item := range items {
					slot := &ScheduleSlot{}

					if status := item.GetStatus(); status != nil {
						slot.Status = freeBusyStatusString(status)
					}

					if startItem := item.GetStart(); startItem != nil {
						if dt := startItem.GetDateTime(); dt != nil {
							tz := ""
							if tzVal := startItem.GetTimeZone(); tzVal != nil {
								tz = *tzVal
							}
							if parsed, err := parseGraphDateTime(*dt, tz); err == nil {
								slot.Start = parsed
							}
						}
					}

					if endItem := item.GetEnd(); endItem != nil {
						if dt := endItem.GetDateTime(); dt != nil {
							tz := ""
							if tzVal := endItem.GetTimeZone(); tzVal != nil {
								tz = *tzVal
							}
							if parsed, err := parseGraphDateTime(*dt, tz); err == nil {
								slot.End = parsed
							}
						}
					}

					// Deliberately NOT reading subject or location (privacy)
					si.ScheduleItems = append(si.ScheduleItems, slot)
				}
			}

			schedules = append(schedules, si)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Info().
		Int("user_count", len(emails)).
		Int("result_count", len(schedules)).
		Msg("Retrieved schedule availability")

	return schedules, nil
}

// freeBusyStatusString converts a FreeBusyStatus enum to a human-readable string.
func freeBusyStatusString(status *models.FreeBusyStatus) string {
	if status == nil {
		return "unknown"
	}
	switch *status {
	case models.FREE_FREEBUSYSTATUS:
		return "free"
	case models.TENTATIVE_FREEBUSYSTATUS:
		return "tentative"
	case models.BUSY_FREEBUSYSTATUS:
		return "busy"
	case models.OOF_FREEBUSYSTATUS:
		return "oof"
	case models.WORKINGELSEWHERE_FREEBUSYSTATUS:
		return "workingElsewhere"
	default:
		return "unknown"
	}
}
