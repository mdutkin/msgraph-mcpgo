package msgraph

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
)

// File represents a OneDrive file
type File struct {
	ID           string    `json:"id"`
	DriveId      string    `json:"driveId,omitempty"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	MimeType     string    `json:"mimeType,omitempty"`
	ModifiedTime time.Time `json:"modifiedDateTime"`
	WebURL       string    `json:"webUrl"`
}

// GetRecentFiles fetches recently modified OneDrive files
func (c *Client) GetRecentFiles(ctx context.Context, top int32) ([]*File, error) {
	var driveItems []models.DriveItemable
	var err error

	err = c.executeWithResilience(ctx, "drive_recent", func() error {
		// First, get the user's default drive ID
		drive, driveErr := c.graphClient.Me().Drive().Get(ctx, nil)
		if driveErr != nil {
			return fmt.Errorf("failed to get user drive: %w", driveErr)
		}

		driveID := drive.GetId()
		if driveID == nil {
			return fmt.Errorf("drive ID is nil")
		}

		// Use /drives/{id}/recent — returns recently accessed files.
		result, fetchErr := c.graphClient.Drives().ByDriveId(*driveID).Recent().GetAsRecentGetResponse(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to fetch recent files: %w", fetchErr)
		}

		driveItems = result.GetValue()
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Convert to our File type
	files := make([]*File, 0, len(driveItems))
	for _, item := range driveItems {
		file := c.convertToFile(item)
		if file != nil {
			files = append(files, file)
		}
	}

	c.logger.Debug().
		Int("count", len(files)).
		Msg("Fetched recent files successfully")

	return files, nil
}

// GetDriveItem fetches metadata for a single drive item (file).
func (c *Client) GetDriveItem(ctx context.Context, driveID, itemID string) (*File, error) {
	var item models.DriveItemable

	err := c.executeWithResilience(ctx, "drive_item_get", func() error {
		result, fetchErr := c.graphClient.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to get drive item: %w", fetchErr)
		}
		item = result
		return nil
	})

	if err != nil {
		return nil, err
	}

	file := c.convertToFile(item)
	if file == nil {
		return nil, fmt.Errorf("failed to convert drive item to file")
	}
	file.DriveId = driveID
	return file, nil
}

// DownloadFileContent downloads the binary content of a file from OneDrive/SharePoint.
func (c *Client) DownloadFileContent(ctx context.Context, driveID, itemID string) ([]byte, error) {
	var data []byte

	err := c.executeWithResilience(ctx, "drive_item_content", func() error {
		// Content().Get() returns []byte directly in the Graph Go SDK
		content, fetchErr := c.graphClient.Drives().ByDriveId(driveID).Items().ByDriveItemId(itemID).Content().Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to download file content: %w", fetchErr)
		}
		if content == nil {
			return fmt.Errorf("file content is nil")
		}
		data = content
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Str("drive_id", driveID).
		Str("item_id", itemID).
		Int("content_bytes", len(data)).
		Msg("Downloaded file content")

	return data, nil
}

// DownloadFileContentWithRecovery retries a 404 download after resolving the
// file's canonical web URL. This handles files that moved after their drive and
// item IDs were returned by search without retrying genuinely unavailable data.
func (c *Client) DownloadFileContentWithRecovery(ctx context.Context, driveID, itemID, webURL string) ([]byte, error) {
	return downloadFileContentWithRecovery(ctx, driveID, itemID, webURL, c.DownloadFileContent, c.ResolveFileByWebURL)
}

func downloadFileContentWithRecovery(
	ctx context.Context,
	driveID, itemID, webURL string,
	download func(context.Context, string, string) ([]byte, error),
	resolve func(context.Context, string) (*File, error),
) ([]byte, error) {
	data, err := download(ctx, driveID, itemID)
	if err == nil || webURL == "" || !isHTTPStatus(err, http.StatusNotFound) {
		return data, err
	}

	resolved, resolveErr := resolve(ctx, webURL)
	if resolveErr != nil || resolved == nil || resolved.DriveId == "" || resolved.ID == "" {
		return nil, err
	}
	if resolved.DriveId == driveID && resolved.ID == itemID {
		return nil, err
	}

	data, retryErr := download(ctx, resolved.DriveId, resolved.ID)
	if retryErr != nil {
		return nil, fmt.Errorf("content download failed after resolving updated file identity: %w", retryErr)
	}
	return data, nil
}

func isHTTPStatus(err error, status int) bool {
	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) || appErr.Details == nil {
		return false
	}
	value, ok := appErr.Details["status_code"]
	if !ok {
		return false
	}
	switch code := value.(type) {
	case int:
		return code == status
	case int32:
		return int(code) == status
	case int64:
		return int(code) == status
	case float64:
		return int(code) == status
	default:
		return false
	}
}

// DownloadFileAsHTML downloads file content converted to HTML by the Graph API.
// This uses the ?format=html query parameter, which triggers server-side
// conversion. Supported for .loop, .fluid, and Office formats.
// The returned bytes are UTF-8 HTML.
func (c *Client) DownloadFileAsHTML(ctx context.Context, driveID, itemID string) ([]byte, error) {
	var data []byte

	err := c.executeWithResilience(ctx, "drive_item_content_html", func() error {
		// Build the URL with ?format=html for server-side conversion.
		// The Graph API returns a 302 redirect to a pre-authenticated download URL;
		// Go's http.Client follows it automatically.
		reqURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/drives/%s/items/%s/content?format=html", driveID, itemID)

		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if reqErr != nil {
			return fmt.Errorf("failed to create request: %w", reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)

		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return fmt.Errorf("failed to download file as HTML: %w", doErr)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("Graph API returned %d for HTML conversion: %s", resp.StatusCode, string(body))
		}

		content, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return fmt.Errorf("failed to read HTML response body: %w", readErr)
		}
		data = content
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Str("drive_id", driveID).
		Str("item_id", itemID).
		Int("content_bytes", len(data)).
		Msg("Downloaded file content as HTML")

	return data, nil
}

// convertToFile converts a Graph SDK drive item to our File type
func (c *Client) convertToFile(item models.DriveItemable) *File {
	if item == nil {
		return nil
	}

	file := &File{}

	if id := item.GetId(); id != nil {
		file.ID = *id
	}

	if name := item.GetName(); name != nil {
		file.Name = *name
	}

	if parentRef := item.GetParentReference(); parentRef != nil {
		if path := parentRef.GetPath(); path != nil {
			file.Path = *path
		}
		if driveId := parentRef.GetDriveId(); driveId != nil {
			file.DriveId = *driveId
		}
	}

	if size := item.GetSize(); size != nil {
		file.Size = *size
	}

	if fileInfo := item.GetFile(); fileInfo != nil {
		if mime := fileInfo.GetMimeType(); mime != nil {
			file.MimeType = *mime
		}
	}

	if modifiedTime := item.GetLastModifiedDateTime(); modifiedTime != nil {
		file.ModifiedTime = *modifiedTime
	}

	if webURL := item.GetWebUrl(); webURL != nil {
		file.WebURL = *webURL
	}

	return file
}

// Drive represents a SharePoint document library or OneDrive
type Drive struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	WebUrl    string `json:"webUrl,omitempty"`
	DriveType string `json:"driveType,omitempty"`
}

// ListSharePointDrives lists all document libraries (drives) for a SharePoint site.
// siteURL should be in the format "https://tenant.sharepoint.com/sites/MySite".
// Requires Sites.Read.All permission.
func (c *Client) ListSharePointDrives(ctx context.Context, siteURL string) ([]*Drive, string, error) {
	// Parse the site URL to extract hostname and server-relative path.
	// Example: "https://contoso.sharepoint.com/sites/DataFabric"
	//   → hostname: "contoso.sharepoint.com"
	//   → path:     "/sites/DataFabric"
	hostname, sitePath, err := parseSiteURL(siteURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid site URL: %w", err)
	}

	var drives []*Drive
	var siteDisplayName string

	err = c.executeWithResilience(ctx, "sharepoint_list_drives", func() error {
		// Resolve the site via GET /sites/{hostname}:/{path}
		siteIdentifier := hostname + ":/" + sitePath + ":"
		site, fetchErr := c.graphClient.Sites().BySiteId(siteIdentifier).Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to resolve SharePoint site %q: %w", siteURL, fetchErr)
		}

		if name := site.GetDisplayName(); name != nil {
			siteDisplayName = *name
		}
		siteID := site.GetId()
		if siteID == nil {
			return fmt.Errorf("site ID is nil for %q", siteURL)
		}

		// List drives for this site
		drivesResult, drivesErr := c.graphClient.Sites().BySiteId(*siteID).Drives().Get(ctx, nil)
		if drivesErr != nil {
			return fmt.Errorf("failed to list drives for site %q: %w", siteURL, drivesErr)
		}

		for _, d := range drivesResult.GetValue() {
			drive := &Drive{}
			if v := d.GetId(); v != nil {
				drive.ID = *v
			}
			if v := d.GetName(); v != nil {
				drive.Name = *v
			}
			if v := d.GetWebUrl(); v != nil {
				drive.WebUrl = *v
			}
			if v := d.GetDriveType(); v != nil {
				drive.DriveType = *v
			}
			drives = append(drives, drive)
		}
		return nil
	})

	if err != nil {
		return nil, "", err
	}

	c.logger.Info().
		Str("site_url", siteURL).
		Int("drive_count", len(drives)).
		Msg("Listed SharePoint drives successfully")

	return drives, siteDisplayName, nil
}

// ResolveFileByWebURL resolves a SharePoint/OneDrive web URL to a File with
// DriveId and ID populated. This allows extracting file content when only the
// webUrl is known (e.g. from search results with missing driveId).
// Requires Sites.Read.All permission.
func (c *Client) ResolveFileByWebURL(ctx context.Context, webURL string) (*File, error) {
	webURL = normalizeWebURLForResolve(webURL)

	// Primary strategy: the /shares endpoint can resolve some web URLs directly.
	// This works well for sharing links and some direct file URLs.
	file, sharesErr := c.resolveBySharesEndpoint(ctx, webURL)
	if sharesErr == nil {
		c.logger.Info().
			Str("web_url", webURL).
			Str("drive_id", file.DriveId).
			Str("item_id", file.ID).
			Msg("Resolved file by web URL via shares endpoint")
		return file, nil
	}

	c.logger.Warn().
		Err(sharesErr).
		Str("web_url", webURL).
		Msg("Shares endpoint resolution failed, trying site/path resolution")

	// Fallback strategy: parse the URL into site + document library + file path,
	// list drives for the site, find the drive whose webUrl is a prefix of the
	// file URL, and resolve the item by path. This is more reliable for direct
	// SharePoint file URLs with special characters or access patterns that the
	// /shares endpoint rejects.
	file, pathErr := c.resolveBySiteAndPath(ctx, webURL)
	if pathErr == nil {
		c.logger.Info().
			Str("web_url", webURL).
			Str("drive_id", file.DriveId).
			Str("item_id", file.ID).
			Msg("Resolved file by web URL via site/path")
		return file, nil
	}

	return nil, fmt.Errorf("failed to resolve file from URL %q: shares endpoint: %w; site/path: %v",
		webURL, sharesErr, pathErr)
}

// resolveBySharesEndpoint tries to resolve a file using the /shares endpoint.
func (c *Client) resolveBySharesEndpoint(ctx context.Context, webURL string) (*File, error) {
	var file *File

	err := c.executeWithResilience(ctx, "resolve_file_by_url_shares", func() error {
		// Encode the web URL as a sharing token for the /shares API
		shareToken := encodeSharingURL(webURL)
		shareID := "u!" + shareToken

		// GET /shares/{shareId}/driveItem
		item, fetchErr := c.graphClient.Shares().BySharedDriveItemId(shareID).DriveItem().Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to resolve file from URL %q: %w", webURL, fetchErr)
		}

		file = c.convertToFile(item)
		if file == nil {
			return fmt.Errorf("failed to convert resolved item to file")
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return file, nil
}

// resolveBySiteAndPath parses a SharePoint file URL into site + drive + path
// and resolves the drive item by path. It is used as a fallback when the
// /shares endpoint cannot resolve the URL.
func (c *Client) resolveBySiteAndPath(ctx context.Context, webURL string) (*File, error) {
	candidates, err := candidateSiteURLs(webURL)
	if err != nil {
		return nil, err
	}

	type bestMatch struct {
		drive        *Drive
		relativePath string
		prefixLen    int
	}
	var best *bestMatch

	for _, siteURL := range candidates {
		drives, _, err := c.ListSharePointDrives(ctx, siteURL)
		if err != nil {
			c.logger.Debug().
				Err(err).
				Str("site_url", siteURL).
				Msg("Candidate site failed to list drives")
			continue
		}

		drive, relativePath, prefixLen, err := findBestDriveByURLPrefix(webURL, drives)
		if err != nil {
			continue
		}

		if best == nil || prefixLen > best.prefixLen {
			best = &bestMatch{
				drive:        drive,
				relativePath: relativePath,
				prefixLen:    prefixLen,
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("could not find a SharePoint drive matching URL %q", webURL)
	}

	itemPathID := buildItemPathID(best.relativePath)

	var item models.DriveItemable
	err = c.executeWithResilience(ctx, "resolve_file_by_path", func() error {
		result, fetchErr := c.graphClient.Drives().ByDriveId(best.drive.ID).Items().ByDriveItemId(itemPathID).Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to resolve item by path %q in drive %q: %w", itemPathID, best.drive.ID, fetchErr)
		}
		item = result
		return nil
	})

	if err != nil {
		return nil, err
	}

	file := c.convertToFile(item)
	if file == nil {
		return nil, fmt.Errorf("failed to convert resolved item to file")
	}
	file.DriveId = best.drive.ID
	return file, nil
}

// normalizeWebURLForResolve performs minimal cleanup of SharePoint URLs so they
// can be compared and parsed consistently.
func normalizeWebURLForResolve(webURL string) string {
	webURL = strings.TrimSpace(webURL)
	webURL = strings.Trim(webURL, `"'`)
	webURL = html.UnescapeString(webURL)
	return webURL
}

// candidateSiteURLs returns all possible SharePoint site URLs extracted from a
// file URL, from shortest to longest. For example,
// https://tenant.sharepoint.com/sites/Site/SubSite/Library/file.txt yields:
//   - https://tenant.sharepoint.com/sites/Site
//   - https://tenant.sharepoint.com/sites/Site/SubSite
func candidateSiteURLs(webURL string) ([]string, error) {
	parsed, err := url.Parse(webURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", webURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("URL missing scheme or host: %q", webURL)
	}

	encodedPath := strings.Trim(parsed.EscapedPath(), "/")
	if encodedPath == "" {
		return nil, fmt.Errorf("URL path is empty: %q", webURL)
	}

	segments := strings.Split(encodedPath, "/")

	// Locate the site root marker (/sites/ or /teams/)
	siteStartIdx := -1
	for i, seg := range segments {
		decoded, _ := url.PathUnescape(seg)
		if decoded == "sites" || decoded == "teams" {
			siteStartIdx = i
			break
		}
	}
	if siteStartIdx < 0 {
		return nil, fmt.Errorf("URL does not contain a SharePoint site path (/sites/ or /teams/): %q", webURL)
	}

	// Minimum site is /sites/SiteName. Any additional path segments could be
	// subsites, so generate a candidate for each possibility up to the segment
	// before the last one (the last segment is the file name).
	maxEndIdx := len(segments) - 1
	if maxEndIdx < siteStartIdx+2 {
		return nil, fmt.Errorf("URL does not contain a complete SharePoint site path: %q", webURL)
	}

	var candidates []string
	for siteEndIdx := siteStartIdx + 2; siteEndIdx <= maxEndIdx; siteEndIdx++ {
		siteSegments := segments[:siteEndIdx]
		siteURL := fmt.Sprintf("%s://%s/%s", parsed.Scheme, parsed.Host, strings.Join(siteSegments, "/"))
		candidates = append(candidates, siteURL)
	}

	return candidates, nil
}

// buildItemPathID converts a URL-encoded relative file path into the Graph API
// drive item identifier format: root:/folder/file.txt:
func buildItemPathID(relativePath string) string {
	segments := strings.Split(relativePath, "/")
	decoded := make([]string, len(segments))
	for i, seg := range segments {
		d, _ := url.PathUnescape(seg)
		decoded[i] = d
	}
	path := strings.Join(decoded, "/")
	return fmt.Sprintf("root:/%s:", path)
}

// findBestDriveByURLPrefix returns the drive whose webUrl is the longest prefix
// of webURL, along with the relative path within that drive and the length of
// the matched prefix. It prefers more specific matches (e.g. a subsite library over
// a parent site library).
func findBestDriveByURLPrefix(webURL string, drives []*Drive) (*Drive, string, int, error) {
	// Decode and normalize so that encoded spaces (%20) and literal spaces match.
	decodedWebURL, _ := url.PathUnescape(webURL)
	decodedWebURL = normalizeWebURLForResolve(decodedWebURL)

	type bestMatch struct {
		drive        *Drive
		relativePath string
		prefixLen    int
	}
	var best *bestMatch

	for _, drive := range drives {
		if drive == nil || drive.ID == "" || drive.WebUrl == "" {
			continue
		}

		decodedDriveURL, _ := url.PathUnescape(drive.WebUrl)
		decodedDriveURL = normalizeWebURLForResolve(decodedDriveURL)
		drivePrefix := strings.TrimSuffix(decodedDriveURL, "/") + "/"

		if strings.HasPrefix(decodedWebURL, drivePrefix) {
			relativePath := strings.TrimPrefix(decodedWebURL, drivePrefix)
			if best == nil || len(drivePrefix) > best.prefixLen {
				best = &bestMatch{
					drive:        drive,
					relativePath: relativePath,
					prefixLen:    len(drivePrefix),
				}
			}
		}
	}

	if best == nil {
		return nil, "", 0, fmt.Errorf("no drive matches URL prefix")
	}

	return best.drive, best.relativePath, best.prefixLen, nil
}

// parseSiteURL extracts the hostname and server-relative path from a SharePoint site URL.
// Example: "https://contoso.sharepoint.com/sites/MySite" → ("contoso.sharepoint.com", "sites/MySite")
func parseSiteURL(siteURL string) (hostname string, sitePath string, err error) {
	// Remove protocol
	u := siteURL
	if idx := strings.Index(u, "://"); idx >= 0 {
		u = u[idx+3:]
	}

	// Split hostname and path
	slashIdx := strings.Index(u, "/")
	if slashIdx < 0 {
		return "", "", fmt.Errorf("no path component in URL %q", siteURL)
	}

	hostname = u[:slashIdx]
	sitePath = strings.TrimPrefix(u[slashIdx:], "/")
	sitePath = strings.TrimSuffix(sitePath, "/")

	if hostname == "" || sitePath == "" {
		return "", "", fmt.Errorf("could not parse hostname or path from URL %q", siteURL)
	}
	return hostname, sitePath, nil
}

// encodeSharingURL converts a URL to a base64url-encoded sharing token
// as required by the /shares API endpoint.
func encodeSharingURL(webURL string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(webURL))
	// Convert to base64url: replace + with -, / with _, remove trailing =
	encoded = strings.ReplaceAll(encoded, "+", "-")
	encoded = strings.ReplaceAll(encoded, "/", "_")
	encoded = strings.TrimRight(encoded, "=")
	return encoded
}

// UploadFile uploads file content to the user's OneDrive.
// folderPath is relative to the drive root (e.g. "Documents/Reports").
// An empty folderPath uploads to the root.
// Supports files up to 4MB (simple upload via PUT).
// Requires Files.ReadWrite (delegated) permission.
func (c *Client) UploadFile(ctx context.Context, folderPath, fileName string, content []byte) (*File, error) {
	var resultFile *File

	err := c.executeWithResilience(ctx, "drive_upload", func() error {
		// Get user's drive ID
		drive, driveErr := c.graphClient.Me().Drive().Get(ctx, nil)
		if driveErr != nil {
			return fmt.Errorf("failed to get user drive: %w", driveErr)
		}
		driveID := drive.GetId()
		if driveID == nil {
			return fmt.Errorf("drive ID is nil")
		}

		// Build the item path in Graph "root:/path/file.txt:" format
		var itemPath string
		if folderPath == "" || folderPath == "/" {
			itemPath = fmt.Sprintf("root:/%s:", fileName)
		} else {
			cleanPath := strings.Trim(folderPath, "/")
			itemPath = fmt.Sprintf("root:/%s/%s:", cleanPath, fileName)
		}

		// Upload via PUT /drives/{driveId}/items/{itemPath}/content
		result, uploadErr := c.graphClient.Drives().ByDriveId(*driveID).Items().ByDriveItemId(itemPath).Content().Put(ctx, content, nil)
		if uploadErr != nil {
			return fmt.Errorf("failed to upload file: %w", uploadErr)
		}

		resultFile = c.convertToFile(result)
		if resultFile != nil {
			resultFile.DriveId = *driveID
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Info().
		Str("filename", fileName).
		Str("folder", folderPath).
		Int("content_size", len(content)).
		Msg("File uploaded successfully")

	return resultFile, nil
}
