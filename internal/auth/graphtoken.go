package auth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Microsoft Graph identifies itself with either audience value depending on the
// token version: the resource URI in a v2.0 token, the resource application ID
// in a v1.0 token.
const (
	graphAudienceURI   = "https://graph.microsoft.com"
	graphAudienceAppID = "00000003-0000-0000-c000-000000000000"
)

// IsGraphAudience reports whether an audience value names Microsoft Graph.
func IsGraphAudience(audience string) bool {
	return audience == graphAudienceURI || audience == graphAudienceAppID
}

// GraphTokenInspector reads the claims of a forwarded Microsoft Graph access
// token.
//
// It does not verify the signature, and it cannot. Microsoft does not publish
// signing keys for tokens issued to the Graph resource and states that a
// resource other than the intended audience must not validate them; a Graph
// token also carries a header nonce that makes third-party verification fail by
// construction.
//
// That is acceptable here precisely because this service forwards the caller's
// token and adds nothing to it. It holds no client secret, performs no
// on-behalf-of exchange, and has no standing permission of its own, so it
// cannot act for anyone. A caller presenting a forged or stolen token obtains
// exactly what the same token would obtain against Microsoft Graph directly,
// and Microsoft Graph is the authority that decides. The checks below are
// therefore fail-fast diagnostics and audit metadata, not authorization. Every
// claim read here is attacker-controlled until Graph accepts the token.
type GraphTokenInspector struct {
	// expectedTenantID, when set, rejects a token whose tid names a different
	// directory. It is a deployment guard rather than a security control: it
	// catches a client pointed at the wrong environment early, instead of
	// after a confusing Graph error.
	expectedTenantID string
}

// NewGraphTokenInspector builds an inspector. An empty tenant ID disables the
// tenant check.
func NewGraphTokenInspector(expectedTenantID string) *GraphTokenInspector {
	return &GraphTokenInspector{expectedTenantID: expectedTenantID}
}

// Inspect parses the token and applies the advisory checks.
//
// The returned claims are unverified. Use them for logging, for the rate limit
// key and for correlation only. Never use them to decide what a caller may do:
// the delegated permissions in the token, enforced by Microsoft Graph, are what
// decide that.
func (i *GraphTokenInspector) Inspect(token string) (*Claims, error) {
	claims, err := parseClaimsUnverified(token)
	if err != nil {
		return nil, err
	}

	if !IsGraphAudience(claims.Audience) {
		return nil, fmt.Errorf(
			"token audience %q is not Microsoft Graph; this server forwards a Graph access token, "+
				"so the client must send one (in LibreChat: {{LIBRECHAT_GRAPH_ACCESS_TOKEN}})",
			claims.Audience)
	}

	if i.expectedTenantID != "" && claims.TenantID != "" && !strings.EqualFold(claims.TenantID, i.expectedTenantID) {
		return nil, fmt.Errorf("token tenant %q does not match the configured tenant", claims.TenantID)
	}

	// Expiry is checked locally only to avoid a pointless Graph round trip.
	// Graph rejects an expired token regardless.
	if claims.Expiration > 0 && time.Now().Unix() >= claims.Expiration {
		return nil, fmt.Errorf("token expired at %s", time.Unix(claims.Expiration, 0).UTC().Format(time.RFC3339))
	}

	if claims.GetUserID() == "" {
		return nil, fmt.Errorf("token carries no user identifier (oid or sub), so the request cannot be attributed")
	}

	return claims, nil
}

// parseClaimsUnverified decodes a JWT payload without verifying the signature.
func parseClaimsUnverified(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("credential is not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("JWT payload is not valid base64url: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("JWT payload is not valid JSON: %w", err)
	}

	claims := &Claims{
		Subject:  stringClaim(raw, "sub"),
		Issuer:   stringClaim(raw, "iss"),
		Email:    firstStringClaim(raw, "email", "upn", "preferred_username"),
		Name:     stringClaim(raw, "name"),
		ObjectID: stringClaim(raw, "oid"),
		TenantID: stringClaim(raw, "tid"),
		Audience: audienceClaim(raw),
	}

	switch exp := raw["exp"].(type) {
	case float64:
		claims.Expiration = int64(exp)
	case int64:
		claims.Expiration = exp
	}

	return claims, nil
}

func stringClaim(raw map[string]any, key string) string {
	s, _ := raw[key].(string)
	return s
}

func firstStringClaim(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := raw[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// audienceClaim reads aud, which RFC 7519 allows to be a string or an array.
func audienceClaim(raw map[string]any) string {
	switch aud := raw["aud"].(type) {
	case string:
		return aud
	case []any:
		for _, entry := range aud {
			if s, ok := entry.(string); ok && IsGraphAudience(s) {
				return s
			}
		}
		if len(aud) > 0 {
			s, _ := aud[0].(string)
			return s
		}
	}
	return ""
}
