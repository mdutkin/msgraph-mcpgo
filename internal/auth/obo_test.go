package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTokenEndpoint stands in for the Entra token endpoint.
type fakeTokenEndpoint struct {
	server   *httptest.Server
	requests atomic.Int64

	mu         sync.Mutex
	expiresIn  int
	failStatus int
	failBody   string
	lastForm   map[string]string
	delay      time.Duration
}

func newFakeTokenEndpoint(t *testing.T) *fakeTokenEndpoint {
	t.Helper()

	f := &fakeTokenEndpoint{expiresIn: 3600}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)

		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		f.mu.Lock()
		form := map[string]string{}
		for k := range r.Form {
			form[k] = r.Form.Get(k)
		}
		f.lastForm = form
		status, body, expiresIn, delay := f.failStatus, f.failBody, f.expiresIn, f.delay
		f.mu.Unlock()

		if delay > 0 {
			time.Sleep(delay)
		}

		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "graph-token-" + fmt.Sprint(f.requests.Load()),
			"token_type":   "Bearer",
			"expires_in":   expiresIn,
			"scope":        "https://graph.microsoft.com/User.Read",
		})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTokenEndpoint) failWith(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStatus, f.failBody = status, body
}

func (f *fakeTokenEndpoint) setExpiresIn(seconds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiresIn = seconds
}

func (f *fakeTokenEndpoint) setDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

func (f *fakeTokenEndpoint) form() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastForm
}

func newTestExchanger(t *testing.T, endpoint *fakeTokenEndpoint, scopes []string) *OBOExchanger {
	t.Helper()

	e, err := NewOBOExchanger(OBOConfig{
		TenantID:     testTenantID,
		ClientID:     testClientID,
		ClientSecret: "a-secret",
		Scopes:       scopes,
		TokenURL:     endpoint.server.URL,
		Logger:       testLogger(),
	})
	if err != nil {
		t.Fatalf("NewOBOExchanger: %v", err)
	}
	return e
}

func TestOBOExchangeSendsTheCorrectGrant(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	exchanger := newTestExchanger(t, endpoint, []string{"https://graph.microsoft.com/Mail.Read"})

	token, err := exchanger.GraphToken(context.Background(), "caller.assertion.value")
	if err != nil {
		t.Fatalf("GraphToken: %v", err)
	}
	if !strings.HasPrefix(token, "graph-token-") {
		t.Fatalf("unexpected token %q", token)
	}

	form := endpoint.form()
	if form["grant_type"] != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Errorf("grant_type = %q", form["grant_type"])
	}
	if form["requested_token_use"] != "on_behalf_of" {
		t.Errorf("requested_token_use = %q", form["requested_token_use"])
	}
	if form["assertion"] != "caller.assertion.value" {
		t.Error("the caller's token was not sent as the assertion")
	}
	if form["client_id"] != testClientID || form["client_secret"] != "a-secret" {
		t.Error("the confidential client credentials were not sent")
	}
	if form["scope"] != "https://graph.microsoft.com/Mail.Read" {
		t.Errorf("scope = %q", form["scope"])
	}
}

// A second call with the same assertion must be served from cache: each
// exchange costs an Entra request and the tenant has a finite budget.
func TestOBOCachesByAssertion(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	exchanger := newTestExchanger(t, endpoint, nil)

	first, err := exchanger.GraphToken(context.Background(), "assertion-a")
	if err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	second, err := exchanger.GraphToken(context.Background(), "assertion-a")
	if err != nil {
		t.Fatalf("second exchange: %v", err)
	}

	if first != second {
		t.Error("the cached token was not reused")
	}
	if got := endpoint.requests.Load(); got != 1 {
		t.Errorf("expected 1 exchange, got %d", got)
	}
}

// Different users must never share a cached token.
func TestOBODoesNotShareTokensBetweenAssertions(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	exchanger := newTestExchanger(t, endpoint, nil)

	a, err := exchanger.GraphToken(context.Background(), "assertion-for-user-a")
	if err != nil {
		t.Fatalf("exchange a: %v", err)
	}
	b, err := exchanger.GraphToken(context.Background(), "assertion-for-user-b")
	if err != nil {
		t.Fatalf("exchange b: %v", err)
	}

	if a == b {
		t.Fatal("two different assertions were served the same delegated token")
	}
	if got := endpoint.requests.Load(); got != 2 {
		t.Errorf("expected 2 exchanges, got %d", got)
	}
}

// Assertions that share a suffix must not collide, which is the bug the digest
// cache key exists to prevent.
func TestOBOSeparatesAssertionsWithSharedSuffix(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	exchanger := newTestExchanger(t, endpoint, nil)

	const suffix = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	a, _ := exchanger.GraphToken(context.Background(), "header-a.payload-a."+suffix)
	b, _ := exchanger.GraphToken(context.Background(), "header-b.payload-b."+suffix)

	if a == b {
		t.Fatal("assertions sharing a suffix were served the same delegated token")
	}
}

// One prompt commonly fans out into several tool calls at once. Each must not
// mint its own token.
func TestOBOCollapsesConcurrentExchanges(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	endpoint.setDelay(50 * time.Millisecond)
	exchanger := newTestExchanger(t, endpoint, nil)

	const callers = 12
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = exchanger.GraphToken(context.Background(), "shared-assertion")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d failed: %v", i, err)
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("caller %d got a different token", i)
		}
	}
	if got := endpoint.requests.Load(); got != 1 {
		t.Errorf("%d concurrent callers caused %d exchanges, expected 1", callers, got)
	}
}

// A token whose remaining lifetime is inside the margin must not be served: a
// Graph call starting then would fail on expiry mid-operation.
func TestOBORefusesToCacheInsideTheExpiryMargin(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	// Shorter than the margin below, so it is never worth caching.
	endpoint.setExpiresIn(60)

	exchanger, err := NewOBOExchanger(OBOConfig{
		TenantID:     testTenantID,
		ClientID:     testClientID,
		ClientSecret: "a-secret",
		ExpiryMargin: 5 * time.Minute,
		TokenURL:     endpoint.server.URL,
		Logger:       testLogger(),
	})
	if err != nil {
		t.Fatalf("NewOBOExchanger: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := exchanger.GraphToken(context.Background(), "assertion-a"); err != nil {
			t.Fatalf("exchange %d: %v", i, err)
		}
	}
	if got := endpoint.requests.Load(); got != 3 {
		t.Errorf("a token inside the expiry margin was cached: %d exchanges for 3 calls", got)
	}
}

// A narrower scope set must not be served a token minted for a wider one.
func TestOBOScopeSetDiscriminatesTheCache(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)

	narrow := newTestExchanger(t, endpoint, []string{"https://graph.microsoft.com/User.Read"})
	wide := newTestExchanger(t, endpoint, []string{
		"https://graph.microsoft.com/User.Read",
		"https://graph.microsoft.com/Mail.Read",
	})

	a, _ := narrow.GraphToken(context.Background(), "assertion-a")
	b, _ := wide.GraphToken(context.Background(), "assertion-a")
	if a == b {
		t.Fatal("two scope sets shared a cached token")
	}
}

// Entra's error codes are the only way to tell these causes apart, so the
// message must carry the remedy rather than a bare failure.
func TestOBOErrorsExplainTheCause(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		contains string
	}{
		{
			name:     "consent missing",
			body:     `{"error":"invalid_grant","error_description":"AADSTS65001: no consent","error_codes":[65001]}`,
			contains: "has not consented",
		},
		{
			name:     "id token used as assertion",
			body:     `{"error":"invalid_grant","error_description":"AADSTS50013: assertion failed","error_codes":[50013]}`,
			contains: "an ID token",
		},
		{
			name:     "wrong audience",
			body:     `{"error":"invalid_request","error_description":"AADSTS700016","error_codes":[700016]}`,
			contains: "does not match AZURE_CLIENT_ID",
		},
		{
			name:     "mfa required",
			body:     `{"error":"interaction_required","error_description":"AADSTS50076","error_codes":[50076]}`,
			contains: "multi-factor authentication",
		},
		{
			name:     "expired assertion",
			body:     `{"error":"invalid_grant","error_description":"AADSTS700082","error_codes":[700082]}`,
			contains: "expired",
		},
		{
			name:     "unmapped code still reports entra's own error",
			body:     `{"error":"invalid_scope","error_description":"AADSTS70011: bad scope","error_codes":[70011]}`,
			contains: "invalid_scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := newFakeTokenEndpoint(t)
			endpoint.failWith(http.StatusBadRequest, tt.body)
			exchanger := newTestExchanger(t, endpoint, nil)

			_, err := exchanger.GraphToken(context.Background(), "assertion-a")
			if err == nil {
				t.Fatal("a failing exchange returned no error")
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error does not explain the cause: %v", err)
			}
		})
	}
}

// A failed exchange must not be cached as if it succeeded.
func TestOBOFailureIsNotCached(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	endpoint.failWith(http.StatusBadRequest, `{"error":"temporarily_unavailable","error_codes":[50000]}`)
	exchanger := newTestExchanger(t, endpoint, nil)

	if _, err := exchanger.GraphToken(context.Background(), "assertion-a"); err == nil {
		t.Fatal("expected the first exchange to fail")
	}

	endpoint.failWith(0, "")
	if _, err := exchanger.GraphToken(context.Background(), "assertion-a"); err != nil {
		t.Fatalf("the retry after a transient failure also failed: %v", err)
	}
}

func TestOBOResponseWithoutATokenIsRejected(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	endpoint.failWith(http.StatusOK, `{"token_type":"Bearer","expires_in":3600}`)
	exchanger := newTestExchanger(t, endpoint, nil)

	if _, err := exchanger.GraphToken(context.Background(), "assertion-a"); err == nil {
		t.Fatal("a response with no access token was accepted")
	}
}

func TestNewOBOExchangerRequiresCredentials(t *testing.T) {
	base := OBOConfig{TenantID: testTenantID, ClientID: testClientID, ClientSecret: "s", Logger: testLogger()}

	noSecret := base
	noSecret.ClientSecret = ""
	if _, err := NewOBOExchanger(noSecret); err == nil {
		t.Error("an exchanger with no client secret was created")
	}

	noClient := base
	noClient.ClientID = ""
	if _, err := NewOBOExchanger(noClient); err == nil {
		t.Error("an exchanger with no client ID was created")
	}
}

func TestOBODefaultScopesAreRequestedWhenUnset(t *testing.T) {
	endpoint := newFakeTokenEndpoint(t)
	exchanger := newTestExchanger(t, endpoint, nil)

	if _, err := exchanger.GraphToken(context.Background(), "assertion-a"); err != nil {
		t.Fatalf("GraphToken: %v", err)
	}
	scope := endpoint.form()["scope"]
	for _, want := range []string{"Mail.Read", "Calendars.ReadWrite", "Sites.Read.All"} {
		if !strings.Contains(scope, want) {
			t.Errorf("default scopes omit %s: %q", want, scope)
		}
	}
}

func TestPassthroughProviderReturnsTheCallerToken(t *testing.T) {
	token, err := PassthroughProvider{}.GraphToken(context.Background(), "caller-token")
	if err != nil || token != "caller-token" {
		t.Fatalf("got %q, %v", token, err)
	}
}
