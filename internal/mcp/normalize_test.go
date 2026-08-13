package mcp

import (
	"testing"
)

func TestNormalizeDateTime(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		tz       string
		expected string
	}{
		{
			name:     "already valid RFC3339 with Z",
			raw:      "2026-04-07T00:00:00Z",
			tz:       "",
			expected: "2026-04-07T00:00:00Z",
		},
		{
			name:     "already valid RFC3339 with offset",
			raw:      "2026-04-07T00:00:00+02:00",
			tz:       "",
			expected: "2026-04-07T00:00:00+02:00",
		},
		{
			name:     "bare ISO-8601 without TZ defaults to Z",
			raw:      "2026-04-07T00:00:00",
			tz:       "",
			expected: "2026-04-07T00:00:00Z",
		},
		{
			name:     "bare ISO-8601 with timezone param uses local offset",
			raw:      "2026-04-07T14:00:00",
			tz:       "Europe/Zurich",
			expected: "2026-04-07T14:00:00+02:00",
		},
		{
			name:     "bare ISO-8601 with fractional seconds defaults to Z",
			raw:      "2026-04-07T00:00:00.000",
			tz:       "",
			expected: "2026-04-07T00:00:00.000Z",
		},
		{
			name:     "empty string passes through",
			raw:      "",
			tz:       "",
			expected: "",
		},
		{
			name:     "non-datetime string passes through unchanged",
			raw:      "not a date",
			tz:       "",
			expected: "not a date",
		},
		{
			name:     "date-only string passes through unchanged",
			raw:      "2026-04-07",
			tz:       "",
			expected: "2026-04-07",
		},
		{
			name:     "invalid timezone falls back to Z",
			raw:      "2026-04-07T12:00:00",
			tz:       "Invalid/Timezone",
			expected: "2026-04-07T12:00:00Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeDateTime(tt.raw, tt.tz)
			if result != tt.expected {
				t.Errorf("normalizeDateTime(%q, %q) = %q, want %q", tt.raw, tt.tz, result, tt.expected)
			}
		})
	}
}

func TestSanitizeEmailSearchQuery(t *testing.T) {
	tests := []struct {
		name            string
		query           string
		wantQuery       string
		wantFilter      string
		wantFilterEmpty bool
	}{
		{
			name:            "plain text query unchanged",
			query:           "budget report Q1",
			wantQuery:       "budget report Q1",
			wantFilterEmpty: true,
		},
		{
			name:       "received:YYYY-MM-DD extracted to filter",
			query:      "received:2026-04-07 meetingrequest",
			wantQuery:  "meetingrequest",
			wantFilter: "receivedDateTime ge 2026-04-07T00:00:00Z and receivedDateTime lt 2026-04-08T00:00:00Z",
		},
		{
			name:       "received:YYYY-MM-DD alone → empty query, filter set",
			query:      "received:2026-04-07",
			wantQuery:  "",
			wantFilter: "receivedDateTime ge 2026-04-07T00:00:00Z and receivedDateTime lt 2026-04-08T00:00:00Z",
		},
		{
			name:       "received range extracted to filter",
			query:      "received:2026-02-01..2026-03-15 project update",
			wantQuery:  "project update",
			wantFilter: "receivedDateTime ge 2026-02-01T00:00:00Z and receivedDateTime le 2026-03-15T23:59:59Z",
		},
		{
			name:       "sent:YYYY-MM-DD extracted to filter",
			query:      "sent:2026-04-01 invoice",
			wantQuery:  "invoice",
			wantFilter: "sentDateTime ge 2026-04-01T00:00:00Z and sentDateTime lt 2026-04-02T00:00:00Z",
		},
		{
			name:            "valid KQL with from: passes through unchanged",
			query:           `from:jane subject:workflow`,
			wantQuery:       `from:jane subject:workflow`,
			wantFilterEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQuery, gotFilter := sanitizeEmailSearchQuery(tt.query)
			if gotQuery != tt.wantQuery {
				t.Errorf("sanitizeEmailSearchQuery(%q) query = %q, want %q", tt.query, gotQuery, tt.wantQuery)
			}
			if tt.wantFilterEmpty {
				if gotFilter != "" {
					t.Errorf("sanitizeEmailSearchQuery(%q) filter = %q, want empty", tt.query, gotFilter)
				}
			} else if gotFilter != tt.wantFilter {
				t.Errorf("sanitizeEmailSearchQuery(%q) filter = %q, want %q", tt.query, gotFilter, tt.wantFilter)
			}
		})
	}
}

func TestIsValidSharePointOrOneDriveURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		isValid bool
	}{
		{
			name:    "valid SharePoint URL",
			input:   "https://contoso.sharepoint.com/sites/Team/Shared%20Documents/file.xlsx",
			isValid: true,
		},
		{
			name:    "valid OneDrive URL",
			input:   "https://contoso-my.sharepoint.com/personal/user/Documents/file.docx",
			isValid: true,
		},
		{
			name:    "Outlook email item ID",
			input:   "AAMkAGVmMDEzMTM4LTZmYWUtNDdkNC1hMDMxLTU1ZDQ3YjM5ZTA2NwBGAAAAAADQGvO5dA1sSadjd1P6izrMBwDr1gOuA7E1TY3nH1p1YmjPAAAAAAEMAADr1gOuA7E1TY3nH1p1YmjPAAAXZC1VAAA=",
			isValid: false,
		},
		{
			name:    "plain path",
			input:   "/sites/Team/file.xlsx",
			isValid: false,
		},
		{
			name:    "ftp URL",
			input:   "ftp://example.com/file.txt",
			isValid: false,
		},
		{
			name:    "root domain only",
			input:   "https://example.com",
			isValid: false,
		},
		{
			name:    "empty string",
			input:   "",
			isValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidSharePointOrOneDriveURL(tt.input)
			if result != tt.isValid {
				t.Errorf("isValidSharePointOrOneDriveURL(%q) = %v, want %v", tt.input, result, tt.isValid)
			}
		})
	}
}

func TestAddOneDay(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"2026-04-07", "2026-04-08"},
		{"2026-02-28", "2026-03-01"}, // non-leap year
		{"2024-02-29", "2024-03-01"}, // leap year
		{"2026-12-31", "2027-01-01"}, // year rollover
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := addOneDay(tt.input)
			if result != tt.expected {
				t.Errorf("addOneDay(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeWebURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "already correct URL with no spaces or entities",
			input:    "https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx",
			expected: "https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx",
		},
		{
			name:     "HTML entity unescaping",
			input:    "https://contoso.sharepoint.com/sites/ClinicalVault/Shared Documents/Shared Content/Process Maps &amp; R&amp;R/file.xlsx",
			expected: "https://contoso.sharepoint.com/sites/ClinicalVault/Shared%20Documents/Shared%20Content/Process%20Maps%20&%20R&R/file.xlsx",
		},
		{
			name:     "surrounding single quotes",
			input:    "'https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx'",
			expected: "https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx",
		},
		{
			name:     "surrounding double quotes",
			input:    `"https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx"`,
			expected: "https://contoso.sharepoint.com/sites/ClinicalVault/Shared/file.xlsx",
		},
		{
			name:     "spaces to percent twenty",
			input:    "https://contoso.sharepoint.com/sites/ClinicalVault/Shared Documents/file.xlsx",
			expected: "https://contoso.sharepoint.com/sites/ClinicalVault/Shared%20Documents/file.xlsx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeWebURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeWebURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
