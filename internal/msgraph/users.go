package msgraph

import (
	"context"
	"fmt"
	"strings"

	abs "github.com/microsoft/kiota-abstractions-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"
)

// User represents a directory user
type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Mail        string `json:"mail"`
	JobTitle    string `json:"jobTitle"`
	Department  string `json:"department"`
}

// SearchUsers searches for users in the tenant by display name or email.
// Requires User.ReadBasic.All (Delegated) permission.
func (c *Client) SearchUsers(ctx context.Context, query string, top int32) ([]*User, error) {
	if top == 0 {
		top = 10
	}

	var results []*User

	err := c.executeWithResilience(ctx, "users_search", func() error {
		safeQuery := strings.ReplaceAll(query, `"`, `\"`)
		search := fmt.Sprintf(`"displayName:%s" OR "mail:%s" OR "userPrincipalName:%s"`, safeQuery, safeQuery, safeQuery)
		selectFields := []string{"id", "displayName", "mail", "jobTitle", "department"}

		// ConsistencyLevel: eventual is required for $search on user properties
		headers := abs.NewRequestHeaders()
		headers.Add("ConsistencyLevel", "eventual")

		cfg := &users.UsersRequestBuilderGetRequestConfiguration{
			Headers: headers,
			QueryParameters: &users.UsersRequestBuilderGetQueryParameters{
				Search: &search,
				Select: selectFields,
				Top:    &top,
			},
		}

		result, err := c.graphClient.Users().Get(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to search users: %w", err)
		}

		for _, u := range result.GetValue() {
			usr := &User{}
			if v := u.GetId(); v != nil {
				usr.ID = *v
			}
			if v := u.GetDisplayName(); v != nil {
				usr.DisplayName = *v
			}
			if v := u.GetMail(); v != nil {
				usr.Mail = *v
			}
			if v := u.GetJobTitle(); v != nil {
				usr.JobTitle = *v
			}
			if v := u.GetDepartment(); v != nil {
				usr.Department = *v
			}
			results = append(results, usr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.logger.Debug().
		Int("count", len(results)).
		Str("query", query).
		Msg("Searched users successfully")

	return results, nil
}

// OrgChart represents a user's organizational context
type OrgChart struct {
	Manager       *User   `json:"manager,omitempty"`
	DirectReports []*User `json:"directReports,omitempty"`
}

// convertToUser is a helper to convert a Graph User object
func (c *Client) convertToUser(u models.Userable) *User {
	if u == nil {
		return nil
	}
	usr := &User{}
	if v := u.GetId(); v != nil {
		usr.ID = *v
	}
	if v := u.GetDisplayName(); v != nil {
		usr.DisplayName = *v
	}
	if v := u.GetMail(); v != nil {
		usr.Mail = *v
	}
	if v := u.GetJobTitle(); v != nil {
		usr.JobTitle = *v
	}
	if v := u.GetDepartment(); v != nil {
		usr.Department = *v
	}
	return usr
}

// GetUserOrgChart fetches a user's manager and direct reports.
// Requires User.Read.All permission.
func (c *Client) GetUserOrgChart(ctx context.Context, userID string) (*OrgChart, error) {
	if userID == "" || userID == "me" {
		userID = "me"
	}

	chart := &OrgChart{}

	err := c.executeWithResilience(ctx, "user_org_chart", func() error {
		var manager models.DirectoryObjectable
		var mgrErr error

		if userID == "me" {
			manager, mgrErr = c.graphClient.Me().Manager().Get(ctx, nil)
		} else {
			manager, mgrErr = c.graphClient.Users().ByUserId(userID).Manager().Get(ctx, nil)
		}

		if mgrErr != nil {
			// Ignore "not found" (user has no manager) but propagate real errors
			if !strings.Contains(mgrErr.Error(), "404") {
				return fmt.Errorf("failed to get manager: %w", mgrErr)
			}
		} else if manager != nil {
			if userable, ok := manager.(models.Userable); ok {
				chart.Manager = c.convertToUser(userable)
			}
		}

		var reports models.DirectoryObjectCollectionResponseable
		var reportsErr error
		if userID == "me" {
			reports, reportsErr = c.graphClient.Me().DirectReports().Get(ctx, nil)
		} else {
			reports, reportsErr = c.graphClient.Users().ByUserId(userID).DirectReports().Get(ctx, nil)
		}

		if reportsErr != nil {
			return fmt.Errorf("failed to get direct reports: %w", reportsErr)
		}
		if reports != nil {
			for _, report := range reports.GetValue() {
				if userable, ok := report.(models.Userable); ok {
					chart.DirectReports = append(chart.DirectReports, c.convertToUser(userable))
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Info().Str("user_id", userID).Msg("Fetched org chart successfully")
	return chart, nil
}

// FindExperts searches for users by topic
func (c *Client) FindExperts(ctx context.Context, topic string) ([]*User, error) {
	var results []*User

	err := c.executeWithResilience(ctx, "find_experts", func() error {
		safeTopic := strings.ReplaceAll(topic, `"`, `\"`)
		search := fmt.Sprintf(`"displayName:%s" OR "department:%s" OR "jobTitle:%s"`, safeTopic, safeTopic, safeTopic)

		headers := abs.NewRequestHeaders()
		headers.Add("ConsistencyLevel", "eventual")

		top := int32(10)
		selectFields := []string{"id", "displayName", "mail", "jobTitle", "department"}
		cfg := &users.UsersRequestBuilderGetRequestConfiguration{
			Headers: headers,
			QueryParameters: &users.UsersRequestBuilderGetQueryParameters{
				Search: &search,
				Top:    &top,
				Select: selectFields,
			},
		}

		result, err := c.graphClient.Users().Get(ctx, cfg)
		if err != nil {
			return fmt.Errorf("failed to find experts: %w", err)
		}

		for _, u := range result.GetValue() {
			if usr := c.convertToUser(u); usr != nil {
				results = append(results, usr)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.logger.Info().Str("topic", topic).Msg("Found experts successfully")
	return results, nil
}
