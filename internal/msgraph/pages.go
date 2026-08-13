package msgraph

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fnfbraga/msgraph-mcpgo/internal/docparse"
)

// PageContent represents the extracted content of a SharePoint site page.
type PageContent struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	WebUrl      string `json:"webUrl,omitempty"`
	PageName    string `json:"pageName,omitempty"`
	Content     string `json:"content"`
	Author      string `json:"author,omitempty"`
}

// ── Raw HTTP response structs for the Pages + canvasLayout API ──────────────

// sitePageResponse models the JSON from GET /sites/{id}/pages/{id}/microsoft.graph.sitePage?$expand=canvasLayout
type sitePageResponse struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	WebUrl       string        `json:"webUrl"`
	CreatedBy    *pageIdentity `json:"createdBy"`
	CanvasLayout *canvasLayout `json:"canvasLayout"`
}

type pageIdentity struct {
	User *struct {
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
	} `json:"user"`
}

type canvasLayout struct {
	HorizontalSections []horizontalSection `json:"horizontalSections"`
}

type horizontalSection struct {
	Columns []sectionColumn `json:"columns"`
}

type sectionColumn struct {
	WebParts []json.RawMessage `json:"webparts"`
}

// textWebPart captures the innerHtml from textWebPart entries.
type textWebPart struct {
	ODataType string `json:"@odata.type"`
	InnerHtml string `json:"innerHtml"`
}

// pagesListResponse models the JSON from GET /sites/{id}/pages
type pagesListResponse struct {
	Value []sitePageSummary `json:"value"`
}

type sitePageSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Name  string `json:"name"`
}

// GetSharePointPageContent reads the full content of a SharePoint site page.
//
// It resolves the site from siteURL, finds the page by filename (pageName)
// or title keyword (searchTitle), fetches the page with $expand=canvasLayout,
// and extracts all textWebPart innerHtml into clean markdown.
//
// At least one of pageName or searchTitle must be provided.
// Requires Sites.Read.All permission.
func (c *Client) GetSharePointPageContent(ctx context.Context, siteURL, pageName, searchTitle string) (*PageContent, *PageNotFoundResult, error) {
	// 1. Resolve site ID
	hostname, sitePath, err := parseSiteURL(siteURL)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid site URL: %w", err)
	}

	var siteID string
	err = c.executeWithResilience(ctx, "sharepoint_page_resolve_site", func() error {
		siteIdentifier := hostname + ":/" + sitePath + ":"
		site, fetchErr := c.graphClient.Sites().BySiteId(siteIdentifier).Get(ctx, nil)
		if fetchErr != nil {
			return fmt.Errorf("failed to resolve SharePoint site %q: %w", siteURL, fetchErr)
		}
		id := site.GetId()
		if id == nil {
			return fmt.Errorf("site ID is nil for %q", siteURL)
		}
		siteID = *id
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	c.logger.Debug().
		Str("site_url", siteURL).
		Str("site_id", siteID).
		Msg("Resolved site for page content")

	// 2. Find the page ID by listing pages and matching name or title
	pageID, notFound, err := c.findPageID(ctx, siteID, pageName, searchTitle)
	if err != nil {
		return nil, nil, err
	}
	if notFound != nil {
		return nil, notFound, nil
	}

	// 3. Fetch the full page with canvasLayout
	page, err := c.fetchPageWithCanvas(ctx, siteID, pageID)
	if err != nil {
		return nil, nil, err
	}

	// 4. Extract text from web parts
	content := extractPageText(page)

	result := &PageContent{
		Title:       page.Title,
		Description: page.Description,
		WebUrl:      page.WebUrl,
		PageName:    page.Name,
		Content:     content,
	}

	if page.CreatedBy != nil && page.CreatedBy.User != nil {
		result.Author = page.CreatedBy.User.DisplayName
	}

	c.logger.Info().
		Str("page_title", result.Title).
		Str("page_name", result.PageName).
		Int("content_length", len(content)).
		Msg("SharePoint page content extracted successfully")

	return result, nil, nil
}

// PageNotFoundResult is returned when findPageID cannot match a page, carrying
// the list of available pages so callers can surface a helpful (non-error) response.
type PageNotFoundResult struct {
	PageName    string
	SearchTitle string
	Available   []sitePageSummary
}

// findPageID lists pages in the site and matches by filename or title keyword.
// When no page matches, it returns ("", nil, &PageNotFoundResult{…}) instead of
// an error so the caller can surface the available pages without triggering an
// MCP error code.
func (c *Client) findPageID(ctx context.Context, siteID, pageName, searchTitle string) (string, *PageNotFoundResult, error) {
	// Build the URL for listing pages
	// GET /sites/{siteId}/pages/microsoft.graph.sitePage
	graphBaseURL := "https://graph.microsoft.com/v1.0"
	url := fmt.Sprintf("%s/sites/%s/pages/microsoft.graph.sitePage?$select=id,title,name&$top=100", graphBaseURL, siteID)

	var pages pagesListResponse
	err := c.executeWithResilience(ctx, "sharepoint_page_list", func() error {
		req, reqErr := http.NewRequestWithContext(ctx, "GET", url, nil)
		if reqErr != nil {
			return fmt.Errorf("failed to create request: %w", reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")

		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return fmt.Errorf("failed to list pages: %w", doErr)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("list pages returned HTTP %d: %s", resp.StatusCode, string(body))
		}

		return json.NewDecoder(resp.Body).Decode(&pages)
	})
	if err != nil {
		return "", nil, err
	}

	c.logger.Debug().
		Int("page_count", len(pages.Value)).
		Str("page_name_filter", pageName).
		Str("search_title_filter", searchTitle).
		Msg("Listed site pages")

	// Match by filename first, then by title keyword
	for _, p := range pages.Value {
		if pageName != "" {
			// Normalise: ensure .aspx suffix
			target := pageName
			if !strings.HasSuffix(strings.ToLower(target), ".aspx") {
				target += ".aspx"
			}
			if strings.EqualFold(p.Name, target) {
				return p.ID, nil, nil
			}
		}
	}

	// Title keyword search (case-insensitive contains)
	if searchTitle != "" {
		needle := strings.ToLower(searchTitle)
		for _, p := range pages.Value {
			if strings.Contains(strings.ToLower(p.Title), needle) {
				return p.ID, nil, nil
			}
			if strings.Contains(strings.ToLower(p.Name), needle) {
				return p.ID, nil, nil
			}
		}
	}

	return "", &PageNotFoundResult{
		PageName:    pageName,
		SearchTitle: searchTitle,
		Available:   pages.Value,
	}, nil
}

// fetchPageWithCanvas fetches a single page with $expand=canvasLayout via raw HTTP.
func (c *Client) fetchPageWithCanvas(ctx context.Context, siteID, pageID string) (*sitePageResponse, error) {
	graphBaseURL := "https://graph.microsoft.com/v1.0"
	url := fmt.Sprintf("%s/sites/%s/pages/%s/microsoft.graph.sitePage?$expand=canvasLayout",
		graphBaseURL, siteID, pageID)

	var page sitePageResponse
	err := c.executeWithResilience(ctx, "sharepoint_page_get", func() error {
		req, reqErr := http.NewRequestWithContext(ctx, "GET", url, nil)
		if reqErr != nil {
			return fmt.Errorf("failed to create request: %w", reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")

		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return fmt.Errorf("failed to get page: %w", doErr)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("get page returned HTTP %d: %s", resp.StatusCode, string(body))
		}

		return json.NewDecoder(resp.Body).Decode(&page)
	})
	if err != nil {
		return nil, err
	}

	return &page, nil
}

// extractPageText walks the canvasLayout and extracts text from textWebParts.
func extractPageText(page *sitePageResponse) string {
	if page.CanvasLayout == nil {
		return ""
	}

	var sb strings.Builder

	for _, section := range page.CanvasLayout.HorizontalSections {
		for _, col := range section.Columns {
			for _, raw := range col.WebParts {
				var wp textWebPart
				if err := json.Unmarshal(raw, &wp); err != nil {
					continue
				}

				// Only process text web parts
				if !strings.Contains(wp.ODataType, "textWebPart") {
					continue
				}

				if wp.InnerHtml == "" {
					continue
				}

				// Convert HTML to markdown
				md, err := docparse.HTMLToMarkdown(wp.InnerHtml)
				if err != nil {
					continue
				}
				if md != "" {
					sb.WriteString(md)
					sb.WriteString("\n\n")
				}
			}
		}
	}

	return strings.TrimSpace(sb.String())
}

// summarisePages returns a comma-separated list of page names for error messages.
func summarisePages(pages []sitePageSummary) string {
	if len(pages) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(pages))
	for _, p := range pages {
		name := p.Name
		if name == "" {
			name = p.Title
		}
		names = append(names, name)
	}
	// Limit to 10 to avoid huge error messages
	if len(names) > 10 {
		names = append(names[:10], fmt.Sprintf("... and %d more", len(names)-10))
	}
	return strings.Join(names, ", ")
}
