package mcp

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ── Datetime normalization ──────────────────────────────────────────────
//
// LLMs sometimes emit ISO-8601 datetimes without a timezone offset
// (e.g. "2026-04-07T00:00:00" instead of "2026-04-07T00:00:00Z").
// Go's time.RFC3339 parsing rejects these. normalizeDateTime fixes them
// transparently so the caller can always parse with time.RFC3339.

// bareISORegex matches an ISO-8601 datetime without a timezone offset:
//
//	2026-04-07T00:00:00       → match
//	2026-04-07T00:00:00Z      → no match (has Z)
//	2026-04-07T00:00:00+02:00 → no match (has offset)
//	2026-04-07T00:00:00.000   → match (fractional seconds, no tz)
var bareISORegex = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?$`,
)

// normalizeDateTime ensures a datetime string can be parsed as RFC3339.
// If the string is a bare ISO-8601 datetime without a timezone offset,
// the provided default timezone (IANA name, e.g. "Europe/Zurich") is used
// to compute the correct UTC offset and append it. If no timezone is provided,
// UTC ("Z") is appended.
//
// Already-valid RFC3339 strings pass through unchanged.
func normalizeDateTime(raw, defaultTimezone string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}

	// Fast path: already parseable as RFC3339 — return as-is.
	if _, err := time.Parse(time.RFC3339, raw); err == nil {
		return raw
	}

	// Check whether it's a bare ISO-8601 datetime missing the TZ suffix.
	if !bareISORegex.MatchString(raw) {
		return raw // not a recognisable pattern; return unchanged for caller to handle
	}

	// If a default timezone was provided, resolve to an offset at that datetime.
	if defaultTimezone != "" {
		loc, err := time.LoadLocation(defaultTimezone)
		if err == nil {
			// Parse the bare datetime as if it were in the specified location.
			parsed, pErr := time.ParseInLocation("2006-01-02T15:04:05", raw, loc)
			if pErr == nil {
				return parsed.Format(time.RFC3339)
			}
		}
	}

	// Fallback: append "Z" (UTC).
	return raw + "Z"
}

// ── Email search query sanitization ─────────────────────────────────────
//
// LLMs sometimes generate malformed KQL for the MS Graph messages $search
// endpoint. Common mistakes:
//   - "received:2026-04-07"         → should be a $filter, not $search KQL
//   - "received:2026-04-07 keyword" → mixed field:value + free text
//
// sanitizeEmailSearchQuery detects these patterns and extracts them
// into a proper OData $filter expression, leaving only free-text keywords
// in the $search query. The caller should merge the returned filter
// with any existing filter using " and ".

// receivedSingleDateRegex matches "received:YYYY-MM-DD" (not a range).
var receivedSingleDateRegex = regexp.MustCompile(
	`(?i)\breceived:(\d{4}-\d{2}-\d{2})\b`,
)

// receivedRangeRegex matches "received:YYYY-MM-DD..YYYY-MM-DD".
var receivedRangeRegex = regexp.MustCompile(
	`(?i)\breceived:(\d{4}-\d{2}-\d{2})\.\.(\d{4}-\d{2}-\d{2})\b`,
)

// sentSingleDateRegex matches "sent:YYYY-MM-DD"
var sentSingleDateRegex = regexp.MustCompile(
	`(?i)\bsent:(\d{4}-\d{2}-\d{2})\b`,
)

// sanitizeEmailSearchQuery inspects a $search query for common LLM mistakes
// and returns:
//   - cleanedQuery: the query with field:value patterns removed (for $search)
//   - extractedFilter: an OData $filter expression extracted from the patterns
//
// If no patterns are found, cleanedQuery == original and extractedFilter == "".
func sanitizeEmailSearchQuery(query string) (cleanedQuery string, extractedFilter string) {
	var filters []string
	cleaned := query

	// Handle received:START..END ranges → convert to $filter with ge/le
	if matches := receivedRangeRegex.FindStringSubmatch(cleaned); len(matches) == 3 {
		startDate := matches[1]
		endDate := matches[2]
		filters = append(filters,
			fmt.Sprintf("receivedDateTime ge %sT00:00:00Z and receivedDateTime le %sT23:59:59Z", startDate, endDate),
		)
		cleaned = receivedRangeRegex.ReplaceAllString(cleaned, "")
	}

	// Handle received:YYYY-MM-DD (single date, not a range — already handled above)
	if matches := receivedSingleDateRegex.FindStringSubmatch(cleaned); len(matches) == 2 {
		date := matches[1]
		filters = append(filters,
			fmt.Sprintf("receivedDateTime ge %sT00:00:00Z and receivedDateTime lt %sT00:00:00Z", date, addOneDay(date)),
		)
		cleaned = receivedSingleDateRegex.ReplaceAllString(cleaned, "")
	}

	// Handle sent:YYYY-MM-DD
	if matches := sentSingleDateRegex.FindStringSubmatch(cleaned); len(matches) == 2 {
		date := matches[1]
		filters = append(filters,
			fmt.Sprintf("sentDateTime ge %sT00:00:00Z and sentDateTime lt %sT00:00:00Z", date, addOneDay(date)),
		)
		cleaned = sentSingleDateRegex.ReplaceAllString(cleaned, "")
	}

	// Clean up remaining whitespace
	cleaned = strings.TrimSpace(cleaned)
	// Collapse multiple spaces
	cleaned = regexp.MustCompile(`\s{2,}`).ReplaceAllString(cleaned, " ")

	return cleaned, strings.Join(filters, " and ")
}

// addOneDay adds one day to a YYYY-MM-DD date string.
func addOneDay(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date // should not happen with regex-validated input
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// ── Web URL normalization ────────────────────────────────────────────────
//
// LLMs sometimes extract SharePoint URLs from HTML emails or documents
// containing HTML entities (e.g., "&amp;" instead of "&"). They may also
// emit URLs with literal spaces instead of "%20", or surrounded by quotes.
// normalizeWebURL cleans these URLs so they can be processed correctly.
func normalizeWebURL(webURL string) string {
	webURL = strings.TrimSpace(webURL)
	// Strip surrounding quotes if present
	webURL = strings.Trim(webURL, `"'`)
	// Unescape HTML entities (e.g. &amp; -> &)
	webURL = html.UnescapeString(webURL)
	// Replace raw spaces with %20. We only do this for actual spaces so
	// we do not affect existing %20 sequences or other encoding.
	webURL = strings.ReplaceAll(webURL, " ", "%20")
	return webURL
}

// isValidSharePointOrOneDriveURL checks whether a web_url looks like a
// OneDrive/SharePoint file URL rather than an Outlook item ID or other
// unrelated value that the LLM might pass to extract_file_content.
func isValidSharePointOrOneDriveURL(webURL string) bool {
	u, err := url.Parse(webURL)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}

	// Reject common Exchange/Outlook message identifiers.
	if strings.HasPrefix(webURL, "AAMk") {
		return false
	}
	// Reject strings with no path at all (likely IDs, not URLs).
	if u.Path == "" || u.Path == "/" {
		return false
	}

	return true
}
