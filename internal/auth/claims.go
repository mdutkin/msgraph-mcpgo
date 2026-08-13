package auth

import (
	"time"
)

// Claims represents the JWT token claims from EntraID
type Claims struct {
	Subject    string `json:"sub"` // User ID
	Audience   string `json:"aud"` // Client ID
	Issuer     string `json:"iss"` // EntraID issuer
	Expiration int64  `json:"exp"` // Unix timestamp
	Email      string `json:"email"`
	Name       string `json:"name"`
	ObjectID   string `json:"oid"` // Azure AD user object ID
	TenantID   string `json:"tid"` // Tenant ID
}

// IsExpired checks if the token is expired
func (c *Claims) IsExpired() bool {
	return time.Now().Unix() >= c.Expiration
}

// ExpiresIn returns the duration until token expiration
func (c *Claims) ExpiresIn() time.Duration {
	expirationTime := time.Unix(c.Expiration, 0)
	return time.Until(expirationTime)
}

// GetUserID returns the best available user identifier
// Prefers ObjectID (oid) over Subject (sub)
func (c *Claims) GetUserID() string {
	if c.ObjectID != "" {
		return c.ObjectID
	}
	return c.Subject
}
