package msgraph

import (
	"context"
	"errors"
	"testing"
)

func TestValidateSearchResult(t *testing.T) {
	ctx := context.Background()

	getDriveItemOK := func(ctx context.Context, driveID, itemID string) (*File, error) {
		return &File{DriveId: driveID, ID: itemID}, nil
	}
	getDriveItemErr := func(ctx context.Context, driveID, itemID string) (*File, error) {
		return nil, errors.New("not found")
	}
	resolveOK := func(ctx context.Context, webURL string) (*File, error) {
		return &File{DriveId: "resolved-drive", ID: "resolved-item", WebURL: webURL}, nil
	}
	resolveErr := func(ctx context.Context, webURL string) (*File, error) {
		return nil, errors.New("unresolvable")
	}

	tests := []struct {
		name         string
		item         *SearchResult
		getDriveItem func(context.Context, string, string) (*File, error)
		resolve      func(context.Context, string) (*File, error)
		wantOK       bool
		wantDriveID  string
		wantItemID   string
	}{
		{
			name: "valid ids pass",
			item: &SearchResult{
				WebUrl:      "https://example.com/file.xlsx",
				DriveId:     "drive-1",
				DriveItemId: "item-1",
			},
			getDriveItem: getDriveItemOK,
			resolve:      resolveErr,
			wantOK:       true,
			wantDriveID:  "drive-1",
			wantItemID:   "item-1",
		},
		{
			name: "stale ids fall back to webUrl",
			item: &SearchResult{
				WebUrl:      "https://example.com/file.xlsx",
				DriveId:     "drive-1",
				DriveItemId: "item-1",
			},
			getDriveItem: getDriveItemErr,
			resolve:      resolveOK,
			wantOK:       true,
			wantDriveID:  "resolved-drive",
			wantItemID:   "resolved-item",
		},
		{
			name: "missing ids resolved from webUrl",
			item: &SearchResult{
				WebUrl: "https://example.com/file.xlsx",
			},
			getDriveItem: getDriveItemErr,
			resolve:      resolveOK,
			wantOK:       true,
			wantDriveID:  "resolved-drive",
			wantItemID:   "resolved-item",
		},
		{
			name: "unresolvable webUrl drops result",
			item: &SearchResult{
				WebUrl: "https://example.com/file.xlsx",
			},
			getDriveItem: getDriveItemErr,
			resolve:      resolveErr,
			wantOK:       false,
		},
		{
			name: "resolution without ids drops result",
			item: &SearchResult{
				WebUrl: "https://example.com/file.xlsx",
			},
			getDriveItem: getDriveItemErr,
			resolve: func(ctx context.Context, webURL string) (*File, error) {
				return &File{DriveId: "", ID: "", WebURL: webURL}, nil
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := validateSearchResult(ctx, tt.item, tt.getDriveItem, tt.resolve)
			if ok != tt.wantOK {
				t.Fatalf("validateSearchResult() ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got.DriveId != tt.wantDriveID {
				t.Errorf("DriveId = %q, want %q", got.DriveId, tt.wantDriveID)
			}
			if got.DriveItemId != tt.wantItemID {
				t.Errorf("DriveItemId = %q, want %q", got.DriveItemId, tt.wantItemID)
			}
		})
	}
}

func TestExtractString(t *testing.T) {
	tests := []struct {
		name string
		data map[string]interface{}
		key  string
		want string
	}{
		{
			name: "string value",
			data: map[string]interface{}{"key": "value"},
			key:  "key",
			want: "value",
		},
		{
			name: "pointer string value",
			data: map[string]interface{}{"key": ptr("value")},
			key:  "key",
			want: "value",
		},
		{
			name: "missing key",
			data: map[string]interface{}{"key": "value"},
			key:  "missing",
			want: "",
		},
		{
			name: "wrong type",
			data: map[string]interface{}{"key": 123},
			key:  "key",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractString(tt.data, tt.key)
			if got != tt.want {
				t.Errorf("extractString(%v, %q) = %q, want %q", tt.data, tt.key, got, tt.want)
			}
		})
	}
}

func TestLastIndexByte(t *testing.T) {
	if got := lastIndexByte("file.name.txt", '.'); got != 9 {
		t.Errorf("lastIndexByte(\"file.name.txt\", '.') = %d, want 9", got)
	}
	if got := lastIndexByte("filename", '.'); got != -1 {
		t.Errorf("lastIndexByte(\"filename\", '.') = %d, want -1", got)
	}
}

func ptr(s string) *string {
	return &s
}
