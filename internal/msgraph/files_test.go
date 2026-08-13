package msgraph

import (
	"context"
	"errors"
	"reflect"
	"testing"

	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
)

func TestDownloadFileContentWithRecovery(t *testing.T) {
	notFound := apperrors.NewClientError(404, errors.New("not found"), map[string]interface{}{"status_code": 404})
	var calls [][2]string
	download := func(_ context.Context, driveID, itemID string) ([]byte, error) {
		calls = append(calls, [2]string{driveID, itemID})
		if driveID == "old-drive" {
			return nil, notFound
		}
		return []byte("content"), nil
	}
	resolve := func(_ context.Context, webURL string) (*File, error) {
		if webURL != "https://example.sharepoint.com/document.docx" {
			t.Fatalf("unexpected web URL: %s", webURL)
		}
		return &File{DriveId: "new-drive", ID: "new-item"}, nil
	}

	data, err := downloadFileContentWithRecovery(
		context.Background(),
		"old-drive",
		"old-item",
		"https://example.sharepoint.com/document.docx",
		download,
		resolve,
	)
	if err != nil {
		t.Fatalf("unexpected recovery error: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("content = %q, want %q", data, "content")
	}
	wantCalls := [][2]string{{"old-drive", "old-item"}, {"new-drive", "new-item"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("download calls = %v, want %v", calls, wantCalls)
	}
}

func TestDownloadFileContentWithRecoveryDoesNotRetryUnchangedIdentity(t *testing.T) {
	notFound := apperrors.NewClientError(404, errors.New("not found"), map[string]interface{}{"status_code": 404})
	downloadCalls := 0
	download := func(_ context.Context, driveID, itemID string) ([]byte, error) {
		downloadCalls++
		return nil, notFound
	}
	resolve := func(_ context.Context, webURL string) (*File, error) {
		return &File{DriveId: "drive", ID: "item"}, nil
	}

	_, err := downloadFileContentWithRecovery(
		context.Background(), "drive", "item", "https://example.sharepoint.com/document.docx", download, resolve,
	)
	if !errors.Is(err, notFound) {
		t.Fatalf("error = %v, want original 404", err)
	}
	if downloadCalls != 1 {
		t.Fatalf("download called %d times, want 1", downloadCalls)
	}
}

func TestNormalizeWebURLForResolve(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no changes",
			input:    "https://contoso.sharepoint.com/sites/Site/file.xlsx",
			expected: "https://contoso.sharepoint.com/sites/Site/file.xlsx",
		},
		{
			name:     "trims spaces",
			input:    "  https://contoso.sharepoint.com/sites/Site/file.xlsx  ",
			expected: "https://contoso.sharepoint.com/sites/Site/file.xlsx",
		},
		{
			name:     "trims surrounding quotes",
			input:    `"https://contoso.sharepoint.com/sites/Site/file.xlsx"`,
			expected: "https://contoso.sharepoint.com/sites/Site/file.xlsx",
		},
		{
			name:     "unescapes HTML entities",
			input:    "https://contoso.sharepoint.com/sites/Site/Process%20Maps%20&amp;%20R&amp;R/file.xlsx",
			expected: "https://contoso.sharepoint.com/sites/Site/Process%20Maps%20&%20R&R/file.xlsx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeWebURLForResolve(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeWebURLForResolve(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCandidateSiteURLs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
		wantErr  bool
	}{
		{
			name:  "simple site",
			input: "https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/file.xlsx",
			expected: []string{
				"https://contoso.sharepoint.com/sites/ClinicalOperations",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents",
			},
		},
		{
			name:  "site with subsite",
			input: "https://contoso.sharepoint.com/sites/ClinicalOperations/SubSite/Shared%20Documents/file.xlsx",
			expected: []string{
				"https://contoso.sharepoint.com/sites/ClinicalOperations",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/SubSite",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/SubSite/Shared%20Documents",
			},
		},
		{
			name:  "teams site",
			input: "https://contoso.sharepoint.com/teams/ProjectA/Shared%20Documents/file.xlsx",
			expected: []string{
				"https://contoso.sharepoint.com/teams/ProjectA",
				"https://contoso.sharepoint.com/teams/ProjectA/Shared%20Documents",
			},
		},
		{
			name:  "deep path",
			input: "https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/CRO/CRO%20Selection/4126-301/file.xlsx",
			expected: []string{
				"https://contoso.sharepoint.com/sites/ClinicalOperations",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/CRO",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/CRO/CRO%20Selection",
				"https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/CRO/CRO%20Selection/4126-301",
			},
		},
		{
			name:    "missing site marker",
			input:   "https://contoso.sharepoint.com/Shared%20Documents/file.xlsx",
			wantErr: true,
		},
		{
			name:    "invalid URL",
			input:   "://bad-url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := candidateSiteURLs(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("candidateSiteURLs(%q) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Errorf("candidateSiteURLs(%q) unexpected error: %v", tt.input, err)
				return
			}
			if len(result) != len(tt.expected) {
				t.Errorf("candidateSiteURLs(%q) returned %d URLs, want %d: %v", tt.input, len(result), len(tt.expected), result)
				return
			}
			for i, want := range tt.expected {
				if result[i] != want {
					t.Errorf("candidateSiteURLs(%q)[%d] = %q, want %q", tt.input, i, result[i], want)
				}
			}
		})
	}
}

func TestBuildItemPathID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple file",
			input:    "folder/file.xlsx",
			expected: "root:/folder/file.xlsx:",
		},
		{
			name:     "encoded spaces",
			input:    "CRO%20Selection/4126-301/file.xlsx",
			expected: "root:/CRO Selection/4126-301/file.xlsx:",
		},
		{
			name:     "encoded plus and ampersand",
			input:    "4126-301%2B302_GxP%20Vendor%20Selection%20%26%20Evaluation%20Tool.xlsx",
			expected: "root:/4126-301+302_GxP Vendor Selection & Evaluation Tool.xlsx:",
		},
		{
			name:     "root file",
			input:    "file.xlsx",
			expected: "root:/file.xlsx:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildItemPathID(tt.input)
			if result != tt.expected {
				t.Errorf("buildItemPathID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFindBestDriveByURLPrefix(t *testing.T) {
	drives := []*Drive{
		{
			ID:        "drive-documents",
			Name:      "Documents",
			WebUrl:    "https://contoso.sharepoint.com/sites/ClinicalOperations/Shared Documents",
			DriveType: "documentLibrary",
		},
		{
			ID:        "drive-archive",
			Name:      "Archive",
			WebUrl:    "https://contoso.sharepoint.com/sites/ClinicalOperations/Archive",
			DriveType: "documentLibrary",
		},
		{
			ID:        "drive-subsite",
			Name:      "Documents",
			WebUrl:    "https://contoso.sharepoint.com/sites/ClinicalOperations/SubSite/Shared Documents",
			DriveType: "documentLibrary",
		},
	}

	tests := []struct {
		name        string
		webURL      string
		wantDriveID string
		wantRelPath string
		wantErr     bool
	}{
		{
			name:        "matches default library",
			webURL:      "https://contoso.sharepoint.com/sites/ClinicalOperations/Shared%20Documents/file.xlsx",
			wantDriveID: "drive-documents",
			wantRelPath: "file.xlsx",
		},
		{
			name:        "matches archive library",
			webURL:      "https://contoso.sharepoint.com/sites/ClinicalOperations/Archive/2024/report.pdf",
			wantDriveID: "drive-archive",
			wantRelPath: "2024/report.pdf",
		},
		{
			name:        "prefers longer prefix (subsite library)",
			webURL:      "https://contoso.sharepoint.com/sites/ClinicalOperations/SubSite/Shared%20Documents/file.xlsx",
			wantDriveID: "drive-subsite",
			wantRelPath: "file.xlsx",
		},
		{
			name:    "no match",
			webURL:  "https://contoso.sharepoint.com/sites/OtherSite/Shared%20Documents/file.xlsx",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drive, relPath, _, err := findBestDriveByURLPrefix(tt.webURL, drives)
			if tt.wantErr {
				if err == nil {
					t.Errorf("findBestDriveByURLPrefix(%q) expected error, got nil", tt.webURL)
				}
				return
			}
			if err != nil {
				t.Errorf("findBestDriveByURLPrefix(%q) unexpected error: %v", tt.webURL, err)
				return
			}
			if drive.ID != tt.wantDriveID {
				t.Errorf("findBestDriveByURLPrefix(%q) drive.ID = %q, want %q", tt.webURL, drive.ID, tt.wantDriveID)
			}
			if relPath != tt.wantRelPath {
				t.Errorf("findBestDriveByURLPrefix(%q) relativePath = %q, want %q", tt.webURL, relPath, tt.wantRelPath)
			}
		})
	}
}
