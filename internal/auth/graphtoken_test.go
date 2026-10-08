package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testTenantID = "11111111-1111-1111-1111-111111111111"

// makeToken builds an unsigned JWT with the given payload. The signature is
// deliberately junk: nothing in this package verifies it, and a test that
// supplied a real signature would imply otherwise.
func makeToken(t *testing.T, payload map[string]any) string {
	t.Helper()

	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}

	header := encode(map[string]any{"alg": "RS256", "typ": "JWT"})
	return header + "." + encode(payload) + ".not-a-real-signature"
}

// graphPayload is a valid forwarded Graph token payload.
func graphPayload() map[string]any {
	return map[string]any{
		"aud":   "https://graph.microsoft.com",
		"iss":   "https://sts.windows.net/" + testTenantID + "/",
		"tid":   testTenantID,
		"oid":   "22222222-2222-2222-2222-222222222222",
		"sub":   "subject-value",
		"upn":   "employee@example.com",
		"name":  "An Employee",
		"scp":   "Mail.Read Calendars.Read",
		"exp":   float64(time.Now().Add(time.Hour).Unix()),
		"appid": "33333333-3333-3333-3333-333333333333",
	}
}

func TestInspectAcceptsForwardedGraphToken(t *testing.T) {
	inspector := NewGraphTokenInspector(testTenantID)

	claims, err := inspector.Inspect(makeToken(t, graphPayload()))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if claims.GetUserID() != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("user id = %q", claims.GetUserID())
	}
	if claims.Email != "employee@example.com" {
		t.Errorf("email = %q", claims.Email)
	}
	if claims.TenantID != testTenantID {
		t.Errorf("tenant = %q", claims.TenantID)
	}
}

// The v1.0 audience is the Graph application ID rather than the resource URI.
func TestInspectAcceptsV1GraphAudience(t *testing.T) {
	payload := graphPayload()
	payload["aud"] = "00000003-0000-0000-c000-000000000000"

	if _, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload)); err != nil {
		t.Fatalf("v1.0 Graph audience rejected: %v", err)
	}
}

// A token for this server's own API, which is what an on-behalf-of design
// would receive, is not usable against Graph and must be refused with an
// explanation rather than forwarded to fail obscurely.
func TestInspectRejectsNonGraphAudience(t *testing.T) {
	payload := graphPayload()
	payload["aud"] = "api://44444444-4444-4444-4444-444444444444"

	_, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload))
	if err == nil {
		t.Fatal("a non-Graph audience was accepted")
	}
	if !strings.Contains(err.Error(), "LIBRECHAT_GRAPH_ACCESS_TOKEN") {
		t.Errorf("error does not say what the client should send: %v", err)
	}
}

func TestInspectRejectsForeignTenant(t *testing.T) {
	payload := graphPayload()
	payload["tid"] = "99999999-9999-9999-9999-999999999999"

	if _, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload)); err == nil {
		t.Fatal("a token from another directory was accepted")
	}
}

func TestInspectIgnoresTenantWhenUnconfigured(t *testing.T) {
	payload := graphPayload()
	payload["tid"] = "99999999-9999-9999-9999-999999999999"

	if _, err := NewGraphTokenInspector("").Inspect(makeToken(t, payload)); err != nil {
		t.Fatalf("tenant check applied with no configured tenant: %v", err)
	}
}

func TestInspectRejectsExpiredToken(t *testing.T) {
	payload := graphPayload()
	payload["exp"] = float64(time.Now().Add(-time.Minute).Unix())

	if _, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload)); err == nil {
		t.Fatal("an expired token was accepted")
	}
}

// Without a user identifier the request cannot be attributed in the audit log
// and the rate limiter has no key, so it is refused.
func TestInspectRejectsTokenWithoutUserIdentifier(t *testing.T) {
	payload := graphPayload()
	delete(payload, "oid")
	delete(payload, "sub")

	if _, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload)); err == nil {
		t.Fatal("a token with no subject was accepted")
	}
}

func TestInspectRejectsMalformedCredentials(t *testing.T) {
	tests := map[string]string{
		"not a jwt":        "opaque-token",
		"two segments":     "header.payload",
		"bad base64":       "header.!!!not-base64!!!.signature",
		"payload not json": "header." + base64.RawURLEncoding.EncodeToString([]byte("plain text")) + ".signature",
		"empty":            "",
	}

	inspector := NewGraphTokenInspector(testTenantID)
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := inspector.Inspect(token); err == nil {
				t.Fatal("a malformed credential was accepted")
			}
		})
	}
}

// RFC 7519 allows aud to be an array; the Graph entry must be found in it.
func TestInspectHandlesAudienceArray(t *testing.T) {
	payload := graphPayload()
	payload["aud"] = []any{"https://other.example.com", "https://graph.microsoft.com"}

	if _, err := NewGraphTokenInspector(testTenantID).Inspect(makeToken(t, payload)); err != nil {
		t.Fatalf("audience array rejected: %v", err)
	}
}

func TestIsGraphAudience(t *testing.T) {
	for _, aud := range []string{"https://graph.microsoft.com", "00000003-0000-0000-c000-000000000000"} {
		if !IsGraphAudience(aud) {
			t.Errorf("%q should be recognised as Graph", aud)
		}
	}
	for _, aud := range []string{"", "https://graph.microsoft.com/", "api://something", "https://management.azure.com"} {
		if IsGraphAudience(aud) {
			t.Errorf("%q should not be recognised as Graph", aud)
		}
	}
}
