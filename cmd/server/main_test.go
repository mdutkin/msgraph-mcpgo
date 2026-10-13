package main

import (
	"testing"

	"github.com/fnfbraga/msgraph-mcpgo/internal/auth"
)

// The metadata document tells a client which token to fetch. If it names the
// Graph scope while the server runs on-behalf-of, the client obtains a
// credential the middleware rejects on audience.
func TestAdvertisedScopesFollowTheAuthenticationMode(t *testing.T) {
	const clientID = "11111111-2222-3333-4444-555555555555"

	cases := []struct {
		mode auth.ValidationMode
		want string
	}{
		{auth.ModeOBO, "api://" + clientID + "/.default"},
		{auth.ModeVerifiedIdentity, "https://graph.microsoft.com/.default"},
		{auth.ModeGraphPassthrough, "https://graph.microsoft.com/.default"},
		{auth.ModeDisabled, "https://graph.microsoft.com/.default"},
	}

	for _, tc := range cases {
		t.Run(tc.mode.String(), func(t *testing.T) {
			got := advertisedScopes(tc.mode, clientID)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("scopes for %s = %v, want [%s]", tc.mode, got, tc.want)
			}
		})
	}
}
