package msgraph

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/search"
)

// SearchResult represents a document or item from enterprise search
type SearchResult struct {
	Name          string    `json:"name"`
	Summary       string    `json:"summary,omitempty"`
	WebUrl        string    `json:"webUrl,omitempty"`
	DriveItemId   string    `json:"driveItemId,omitempty"`
	DriveId       string    `json:"driveId,omitempty"`
	FileExtension string    `json:"fileExtension,omitempty"`
	LastModified  time.Time `json:"lastModifiedDateTime,omitempty"`
	Size          int64     `json:"size,omitempty"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	SiteName      string    `json:"siteName,omitempty"`
	ResourceType  string    `json:"resourceType,omitempty"`
}

// SearchSharepoint searches across SharePoint, OneDrive, and Teams for documents.
// Requires Sites.Read.All permission.
func (c *Client) SearchSharepoint(ctx context.Context, query string, top int32) ([]*SearchResult, error) {
	if top <= 0 {
		top = 10
	}

	var results []*SearchResult

	err := c.executeWithResilience(ctx, "search_sharepoint", func() error {
		requestBody := search.NewQueryPostRequestBody()
		request := models.NewSearchRequest()

		// Set query string
		searchQuery := models.NewSearchQuery()
		searchQuery.SetQueryString(&query)
		request.SetQuery(searchQuery)

		// Set entity types
		entityTypes := []models.EntityType{
			models.DRIVEITEM_ENTITYTYPE,
			models.LISTITEM_ENTITYTYPE,
			models.SITE_ENTITYTYPE,
		}
		request.SetEntityTypes(entityTypes)

		// NOTE: Do NOT use SetFields() here. The `fields` property on SearchRequest
		// is for SharePoint-specific managed properties (custom columns), NOT for
		// standard driveItem properties like name, webUrl, or parentReference.
		// Standard driveItem properties are deserialized into typed DriveItem objects
		// by the SDK's discriminator (via @odata.type).

		// Set size (top)
		request.SetSize(&top)

		requestBody.SetRequests([]models.SearchRequestable{request})

		// Post to /search/query
		searchResponse, err := c.graphClient.Search().Query().Post(ctx, requestBody, nil)
		if err != nil {
			return fmt.Errorf("failed to search sharepoint: %w", err)
		}

		// Parse response
		if value := searchResponse.GetValue(); value != nil {
			for _, val := range value {
				if hitsContainers := val.GetHitsContainers(); hitsContainers != nil {
					for _, container := range hitsContainers {
						if hits := container.GetHits(); hits != nil {
							for _, hit := range hits {
								item := &SearchResult{}

								// Optional summary provided by search API
								if summary := hit.GetSummary(); summary != nil {
									item.Summary = *summary
								}

								// Parse the resource.
								// The Graph SDK deserializes search hit resources using the @odata.type
								// discriminator. For driveItem results, this creates a full DriveItem
								// object where properties are in typed getters (GetName(), GetWebUrl(),
								// GetParentReference()), NOT in additionalData.
								if resource := hit.GetResource(); resource != nil {
									if id := resource.GetId(); id != nil {
										item.DriveItemId = *id
									}

									// Detect resource type from @odata.type
									if data := resource.GetAdditionalData(); data != nil {
										if odataType := extractString(data, "@odata.type"); odataType != "" {
											switch {
											case strings.Contains(odataType, "driveItem"):
												item.ResourceType = "driveItem"
											case strings.Contains(odataType, "listItem"):
												item.ResourceType = "listItem"
											case strings.Contains(odataType, "site"):
												item.ResourceType = "site"
											default:
												item.ResourceType = odataType
											}
										}
									}

									// Try typed DriveItem first (most common for SharePoint/OneDrive search)
									if driveItem, ok := resource.(models.DriveItemable); ok {
										c.logger.Debug().
											Str("item_id", item.DriveItemId).
											Msg("Resource is a typed DriveItem — using typed getters")

										if name := driveItem.GetName(); name != nil {
											item.Name = *name
										}
										if webUrl := driveItem.GetWebUrl(); webUrl != nil {
											item.WebUrl = *webUrl
										}
										if size := driveItem.GetSize(); size != nil {
											item.Size = *size
										}
										if modified := driveItem.GetLastModifiedDateTime(); modified != nil {
											item.LastModified = *modified
										}
										if fileInfo := driveItem.GetFile(); fileInfo != nil {
											// No direct fileExtension getter on File; derive from name
										}
										if createdBy := driveItem.GetCreatedBy(); createdBy != nil {
											if user := createdBy.GetUser(); user != nil {
												if name := user.GetDisplayName(); name != nil {
													item.CreatedBy = *name
												}
											}
										}
										if parentRef := driveItem.GetParentReference(); parentRef != nil {
											if driveId := parentRef.GetDriveId(); driveId != nil {
												item.DriveId = *driveId
											}
											if name := parentRef.GetName(); name != nil {
												item.SiteName = *name
											}
										}

										// Derive file extension from name
										if item.Name != "" {
											if dotIdx := lastIndexByte(item.Name, '.'); dotIdx >= 0 {
												item.FileExtension = item.Name[dotIdx+1:]
											}
										}
									} else {
										// Fallback: resource is a generic Entity — try additionalData
										c.logger.Debug().
											Str("item_id", item.DriveItemId).
											Msg("Resource is a generic Entity — using additionalData")

										if data := resource.GetAdditionalData(); data != nil {
											// Debug: log available keys
											keys := make([]string, 0, len(data))
											for k := range data {
												keys = append(keys, k)
											}
											sort.Strings(keys)
											c.logger.Debug().
												Str("item_id", item.DriveItemId).
												Strs("additionalData_keys", keys).
												Msg("Fallback additionalData keys")

											item.Name = extractString(data, "name")
											item.WebUrl = extractString(data, "webUrl")
											item.FileExtension = extractString(data, "fileExtension")

											// Size
											if size, ok := data["size"].(float64); ok {
												item.Size = int64(size)
											} else if size, ok := data["size"].(int64); ok {
												item.Size = size
											}

											// Last modified
											if lm := extractString(data, "lastModifiedDateTime"); lm != "" {
												if t, err := time.Parse(time.RFC3339, lm); err == nil {
													item.LastModified = t
												}
											}

											// Created by
											if createdBy, ok := data["createdBy"]; ok {
												if cbMap, ok := createdBy.(map[string]interface{}); ok {
													if user, ok := cbMap["user"].(map[string]interface{}); ok {
														item.CreatedBy = extractString(user, "displayName")
													}
												}
											}

											// Extract parentReference — driveId and siteName
											if parentRef, ok := data["parentReference"]; ok {
												if refMap, ok := parentRef.(map[string]interface{}); ok {
													item.DriveId = extractString(refMap, "driveId")
													item.SiteName = extractString(refMap, "name")
												}
											}
										}
									}
								}

								// Log result summary
								c.logger.Debug().
									Str("name", item.Name).
									Str("web_url", item.WebUrl).
									Str("drive_id", item.DriveId).
									Str("item_id", item.DriveItemId).
									Msg("Search result parsed")

								results = append(results, item)
							}
						}
					}
				}
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	// Strict validation: every returned result must be resolvable to a usable
	// driveId + driveItemId. Search hits sometimes lack these identifiers, or
	// contain stale/invalid metadata. We either backfill from webUrl resolution
	// or drop the result so the LLM never receives an unusable link.
	validated := make([]*SearchResult, 0, len(results))
	dropped := 0
	for _, item := range results {
		if item.WebUrl == "" {
			c.logger.Warn().
				Str("item_id", item.DriveItemId).
				Msg("Dropping search result with no webUrl")
			dropped++
			continue
		}

		backfilled, ok := c.validateAndBackfillSearchResult(ctx, item)
		if !ok {
			c.logger.Warn().
				Str("name", item.Name).
				Str("web_url", item.WebUrl).
				Msg("Dropping unresolvable search result")
			dropped++
			continue
		}
		validated = append(validated, backfilled)
	}

	if dropped > 0 {
		c.logger.Info().
			Int("dropped", dropped).
			Int("kept", len(validated)).
			Msg("Validated SharePoint search results")
	}

	results = validated

	c.logger.Info().
		Str("query", query).
		Int("count", len(results)).
		Msg("Searched SharePoint successfully")

	return results, nil
}

// validateAndBackfillSearchResult checks that a SearchResult can be resolved to
// a real drive item. It uses GetDriveItem when IDs are present, falling back to
// ResolveFileByWebURL to backfill missing or invalid identifiers.
func (c *Client) validateAndBackfillSearchResult(ctx context.Context, item *SearchResult) (*SearchResult, bool) {
	return validateSearchResult(ctx, item, c.GetDriveItem, c.ResolveFileByWebURL)
}

// validateSearchResult implements the validation logic with injectable resolver
// functions so it can be unit-tested without a live Graph client.
func validateSearchResult(
	ctx context.Context,
	item *SearchResult,
	getDriveItem func(ctx context.Context, driveID, itemID string) (*File, error),
	resolveFileByWebURL func(ctx context.Context, webURL string) (*File, error),
) (*SearchResult, bool) {
	if item.DriveId != "" && item.DriveItemId != "" {
		_, err := getDriveItem(ctx, item.DriveId, item.DriveItemId)
		if err == nil {
			return item, true
		}
		// IDs may be stale; fall through and try webUrl resolution.
	}

	resolved, err := resolveFileByWebURL(ctx, item.WebUrl)
	if err != nil {
		return nil, false
	}
	if resolved != nil {
		if resolved.DriveId != "" {
			item.DriveId = resolved.DriveId
		}
		if resolved.ID != "" {
			item.DriveItemId = resolved.ID
		}
	}

	// Final check: we must have both IDs to consider the result usable.
	if item.DriveId == "" || item.DriveItemId == "" {
		return nil, false
	}
	return item, true
}

// extractString safely extracts a string value from a map, handling both
// string and *string types that the Graph SDK may return in additionalData.
func extractString(data map[string]interface{}, key string) string {
	if v, ok := data[key].(*string); ok && v != nil {
		return *v
	}
	if v, ok := data[key].(string); ok {
		return v
	}
	return ""
}

// lastIndexByte returns the index of the last occurrence of c in s, or -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
