package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

// DefaultGraphScopes are the delegated Microsoft Graph permissions requested
// during the exchange.
//
// The set is explicit rather than ".default" so that the delegated token this
// service obtains is bounded by configuration here, not by whatever happens to
// be consented on the app registration. Narrowing GRAPH_SCOPES to the
// permissions the exposed tools actually need is the lever that makes a
// withheld tool also a withheld permission: excluding search_emails in
// tools.yaml stops the model calling it, while removing Mail.Read from this
// list stops the token being able to.
var DefaultGraphScopes = []string{
	"https://graph.microsoft.com/User.Read",
	"https://graph.microsoft.com/User.Read.All",
	"https://graph.microsoft.com/Mail.Read",
	"https://graph.microsoft.com/Mail.Send",
	"https://graph.microsoft.com/Calendars.Read",
	"https://graph.microsoft.com/Calendars.ReadWrite",
	"https://graph.microsoft.com/Files.Read.All",
	"https://graph.microsoft.com/Files.ReadWrite",
	"https://graph.microsoft.com/Chat.Read",
	"https://graph.microsoft.com/ChatMessage.Send",
	"https://graph.microsoft.com/OnlineMeetings.Read",
	"https://graph.microsoft.com/OnlineMeetingTranscript.Read.All",
	"https://graph.microsoft.com/Sites.Read.All",
}

// GraphCredentialProvider turns the credential a caller presented into a
// credential usable against Microsoft Graph.
//
// The indirection exists so that the MCP layer does not need to know which
// authentication mode is in force, and so that the exchange happens lazily:
// initialize and tools/list need no Graph call, and must not pay for a token
// round trip they will not use.
type GraphCredentialProvider interface {
	// GraphToken returns a Microsoft Graph access token for the caller.
	GraphToken(ctx context.Context, callerToken string) (string, error)
}

// PassthroughProvider forwards the caller's credential unchanged. Used when the
// caller already presents a Graph token.
type PassthroughProvider struct{}

// GraphToken returns the caller's own token.
func (PassthroughProvider) GraphToken(_ context.Context, callerToken string) (string, error) {
	return callerToken, nil
}

// OBOExchanger converts a caller's access token for this API into a delegated
// Microsoft Graph token, using the OAuth 2.0 On-Behalf-Of flow.
//
// This reintroduces standing privilege, deliberately and with eyes open. The
// client secret can mint a delegated Graph token for any user whose assertion
// this service has seen, so compromise of the task, the image or the secret
// means access to those mailboxes, calendars, chats and files without the user
// present. The alternative is for the calling application to perform the
// exchange and send the resulting Graph token, which keeps this service free of
// credentials. That alternative is still available as AUTH_MODE=graph_passthrough
// or verified_identity.
//
// Given the privilege, two things are not optional. The assertion's signature
// is verified before it is sent to Microsoft Entra, so the service never
// forwards an unverified string to the token endpoint and never exchanges a
// token minted for a different audience. And the exchanged token is cached
// under a digest of the assertion, never under the assertion itself or a
// fragment of it.
type OBOExchanger struct {
	tenantID     string
	clientID     string
	clientSecret string
	scopes       []string
	tokenURL     string

	client *http.Client

	// inFlight collapses concurrent exchanges for the same assertion into one
	// request. An MCP prompt commonly fans out several tool calls at once, and
	// without this each one would mint its own token and consume Entra request
	// budget for a credential the others already hold.
	inFlight singleflight.Group

	mu     sync.Mutex
	cached map[string]cachedToken

	// expiryMargin is subtracted from the token lifetime so a token is never
	// handed out with only seconds left. A Graph call that starts inside the
	// margin would otherwise fail on expiry mid-operation.
	expiryMargin time.Duration

	logger  *zerolog.Logger
	metrics *observability.Metrics
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// OBOConfig configures the exchanger.
type OBOConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string

	// Scopes are the delegated Graph permissions requested. Empty selects
	// DefaultGraphScopes.
	Scopes []string

	// ExpiryMargin is how long before true expiry a cached token stops being
	// served. Zero selects five minutes.
	ExpiryMargin time.Duration

	// TokenURL overrides the Entra token endpoint. A sovereign cloud needs its
	// own; tests use it too.
	TokenURL string

	HTTPClient *http.Client
	Logger     *zerolog.Logger
	Metrics    *observability.Metrics
}

// NewOBOExchanger builds an exchanger.
func NewOBOExchanger(cfg OBOConfig) (*OBOExchanger, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("obo exchanger: logger is required")
	}
	if cfg.TenantID == "" || cfg.ClientID == "" {
		return nil, fmt.Errorf("obo exchanger: tenant ID and client ID are required")
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf(
			"obo exchanger: client secret is required; the on-behalf-of grant authenticates this " +
				"service as a confidential client")
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultGraphScopes
	}

	expiryMargin := cfg.ExpiryMargin
	if expiryMargin <= 0 {
		expiryMargin = 5 * time.Minute
	}

	tokenURL := cfg.TokenURL
	if tokenURL == "" {
		tokenURL = fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token",
			url.PathEscape(cfg.TenantID))
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}

	return &OBOExchanger{
		tenantID:     cfg.TenantID,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		scopes:       scopes,
		tokenURL:     tokenURL,
		client:       httpClient,
		cached:       make(map[string]cachedToken),
		expiryMargin: expiryMargin,
		logger:       cfg.Logger,
		metrics:      cfg.Metrics,
	}, nil
}

// Scopes returns the delegated permissions this exchanger requests.
func (e *OBOExchanger) Scopes() []string {
	return e.scopes
}

// GraphToken exchanges the caller's assertion for a delegated Graph token,
// serving a cached one when it is still comfortably valid.
func (e *OBOExchanger) GraphToken(ctx context.Context, callerToken string) (string, error) {
	key := cacheKey("obo", callerToken, scopeDiscriminator(e.scopes))

	if token, ok := e.fromCache(key); ok {
		e.count("cache_hit")
		return token, nil
	}

	// Concurrent callers with the same assertion share one exchange. The
	// result is shared too, which is safe because the key already binds the
	// assertion and the scope set.
	result, err, _ := e.inFlight.Do(key, func() (any, error) {
		// Another goroutine may have populated the cache while this one
		// waited for the lock inside Do.
		if token, ok := e.fromCache(key); ok {
			return token, nil
		}

		token, expiresIn, err := e.exchange(ctx, callerToken)
		if err != nil {
			return nil, err
		}

		e.store(key, token, expiresIn)
		return token, nil
	})
	if err != nil {
		e.count("error")
		return "", err
	}

	e.count("exchanged")
	return result.(string), nil
}

func (e *OBOExchanger) fromCache(key string) (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, ok := e.cached[key]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		delete(e.cached, key)
		return "", false
	}
	return entry.token, true
}

func (e *OBOExchanger) store(key, token string, expiresIn int) {
	lifetime := time.Duration(expiresIn)*time.Second - e.expiryMargin
	if lifetime <= 0 {
		// Too short to be worth holding; the next call exchanges again.
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.cached[key] = cachedToken{token: token, expiresAt: time.Now().Add(lifetime)}
	e.evictExpiredLocked()
}

// evictExpiredLocked drops entries that have passed their usable lifetime.
// Without it the map grows once per distinct assertion, and an assertion
// changes every time the caller's own token is refreshed, so the growth is
// unbounded over the life of a task.
func (e *OBOExchanger) evictExpiredLocked() {
	now := time.Now()
	for key, entry := range e.cached {
		if now.After(entry.expiresAt) {
			delete(e.cached, key)
		}
	}
}

// oboTokenResponse is the success body from the token endpoint.
type oboTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// tokenEndpointError is the failure body.
type tokenEndpointError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
	TraceID          string `json:"trace_id"`
	CorrelationID    string `json:"correlation_id"`
}

// exchange performs the on-behalf-of grant.
func (e *OBOExchanger) exchange(ctx context.Context, assertion string) (string, int, error) {
	logger := observability.LoggerFromContext(ctx, *e.logger)

	form := url.Values{
		"grant_type":          {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"client_id":           {e.clientID},
		"client_secret":       {e.clientSecret},
		"assertion":           {assertion},
		"scope":               {strings.Join(e.scopes, " ")},
		"requested_token_use": {"on_behalf_of"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build on-behalf-of request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("on-behalf-of request failed: %w", err)
	}
	defer resp.Body.Close()

	// The body is read in full before branching so a failure can be reported
	// with Entra's own diagnostics, which are the only way to tell consent,
	// audience and conditional-access failures apart.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("read on-behalf-of response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, e.describeFailure(logger, resp.StatusCode, body)
	}

	var parsed oboTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", 0, fmt.Errorf("decode on-behalf-of response: %w", err)
	}
	if parsed.AccessToken == "" {
		return "", 0, fmt.Errorf("on-behalf-of response carried no access token")
	}

	logger.Debug().
		Int("expires_in", parsed.ExpiresIn).
		Str("granted_scope", parsed.Scope).
		Msg("Delegated Graph token obtained")

	return parsed.AccessToken, parsed.ExpiresIn, nil
}

// describeFailure turns an Entra error body into something an operator can act
// on. The token endpoint's own error codes distinguish causes that look
// identical from the outside, and the common ones have specific remedies.
func (e *OBOExchanger) describeFailure(logger zerolog.Logger, status int, body []byte) error {
	var detail tokenEndpointError
	if err := json.Unmarshal(body, &detail); err != nil || detail.Error == "" {
		logger.Error().
			Int("status_code", status).
			Msg("On-behalf-of exchange failed with a non-JSON error")
		return fmt.Errorf("on-behalf-of exchange failed with status %d", status)
	}

	logger.Error().
		Int("status_code", status).
		Str("entra_error", detail.Error).
		Str("entra_error_description", detail.ErrorDescription).
		Ints("entra_error_codes", detail.ErrorCodes).
		Str("entra_trace_id", detail.TraceID).
		Str("entra_correlation_id", detail.CorrelationID).
		Msg("On-behalf-of exchange failed")

	return fmt.Errorf("on-behalf-of exchange failed: %s (%s)%s",
		detail.Error, firstLine(detail.ErrorDescription), remedyFor(detail))
}

// remedyFor appends a hint for the Entra error codes with a known cause.
func remedyFor(detail tokenEndpointError) string {
	for _, code := range detail.ErrorCodes {
		switch code {
		case 65001:
			return ". The user or an administrator has not consented to the requested Graph " +
				"permissions. Grant consent on the app registration, or narrow GRAPH_SCOPES."
		case 50013:
			return ". The assertion was rejected. The on-behalf-of grant requires an ACCESS token " +
				"issued for this application, carrying at least one delegated scope; an ID token " +
				"cannot be exchanged."
		case 700016, 700025:
			return ". The audience of the assertion does not match AZURE_CLIENT_ID, so this " +
				"service is not the application the token was issued for and cannot exchange it."
		case 50076, 50079, 53003:
			return ". Conditional access or multi-factor authentication is required. The user must " +
				"reauthenticate interactively; this service cannot satisfy it."
		case 700082, 50173:
			return ". The assertion has expired. The client must send a freshly acquired token."
		}
	}

	if strings.Contains(detail.Error, "invalid_grant") {
		return ". The assertion is not acceptable as an on-behalf-of grant; confirm it is an " +
			"access token for this application rather than an ID token."
	}
	return ""
}

// firstLine trims an Entra description to its first line, which carries the
// AADSTS code and the summary; the remainder is a timestamp and trace ids that
// are already logged as separate fields.
func firstLine(s string) string {
	if idx := strings.IndexAny(s, "\r\n"); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

func (e *OBOExchanger) count(outcome string) {
	if e.metrics != nil {
		e.metrics.OBOExchangeTotal.WithLabelValues(outcome).Inc()
	}
}
