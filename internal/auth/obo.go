package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	cache "github.com/patrickmn/go-cache"
	"github.com/rs/zerolog"
)

// OBOExchanger exchanges tokens via the OAuth 2.0 On-Behalf-Of flow
type OBOExchanger struct {
	tenantID     string
	clientID     string
	clientSecret string
	scopes       []string
	tokenCache   *cache.Cache
	logger       *zerolog.Logger
}

// oboTokenResponse is the response from the token endpoint
type oboTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

type tokenEndpointError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorCodes       []int  `json:"error_codes"`
	Timestamp        string `json:"timestamp"`
	TraceID          string `json:"trace_id"`
	CorrelationID    string `json:"correlation_id"`
}

// graphScopes are the MS Graph permissions needed by this server.
// These must cover every Graph API endpoint used by the MCP tools.
var graphScopes = []string{
	// User profile
	"https://graph.microsoft.com/User.Read",
	"https://graph.microsoft.com/User.Read.All",
	// Mail
	"https://graph.microsoft.com/Mail.Read",
	// Calendar
	"https://graph.microsoft.com/Calendars.Read",
	"https://graph.microsoft.com/Calendars.ReadWrite",
	// Mail send
	"https://graph.microsoft.com/Mail.Send",
	// Files / OneDrive
	"https://graph.microsoft.com/Files.Read.All",
	"https://graph.microsoft.com/Files.ReadWrite",
	// Teams Chat
	"https://graph.microsoft.com/Chat.Read",
	"https://graph.microsoft.com/ChatMessage.Send",
	// Online Meetings & Transcripts
	"https://graph.microsoft.com/OnlineMeetings.Read",
	"https://graph.microsoft.com/OnlineMeetingTranscript.Read.All",
	// SharePoint / Search
	"https://graph.microsoft.com/Sites.Read.All",
	// Refresh
	"offline_access",
}

// NewOBOExchanger creates a new OBO token exchanger
func NewOBOExchanger(tenantID, clientID, clientSecret string, logger *zerolog.Logger) *OBOExchanger {
	return &OBOExchanger{
		tenantID:     tenantID,
		clientID:     clientID,
		clientSecret: clientSecret,
		scopes:       graphScopes,
		tokenCache:   cache.New(5*time.Minute, 10*time.Minute),
		logger:       logger,
	}
}

// ExchangeForGraphToken exchanges the incoming token for a MS Graph token via OBO
func (e *OBOExchanger) ExchangeForGraphToken(ctx context.Context, incomingToken string) (string, error) {
	logger := observability.LoggerFromContext(ctx, *e.logger)

	if claims, err := parseJWTDebugClaims(incomingToken); err == nil {
		logEvt := logger.Debug().
			Str("incoming_token_iss", claims.Issuer).
			Str("incoming_token_tid", claims.TenantID).
			Str("incoming_token_aud", claims.Audience).
			Str("incoming_token_scp", claims.Scope).
			Msg

		if claims.Audience == "https://graph.microsoft.com" || claims.Audience == "00000003-0000-0000-c000-000000000000" {
			logger.Warn().
				Str("incoming_token_aud", claims.Audience).
				Msg("Incoming token audience is Graph. OBO expects token audience to be this API (AZURE_CLIENT_ID).")
		}
		logEvt("Preparing OBO token exchange")
	} else {
		logger.Debug().Err(err).Msg("Could not parse incoming token payload for OBO debug")
	}

	// Use token hash as cache key to avoid storing full token in key
	cacheKey := fmt.Sprintf("obo:%d", len(incomingToken))
	if len(incomingToken) > 20 {
		cacheKey = fmt.Sprintf("obo:%s", incomingToken[len(incomingToken)-20:])
	}

	if cached, found := e.tokenCache.Get(cacheKey); found {
		logger.Debug().Msg("OBO token retrieved from cache")
		return cached.(string), nil
	}

	token, expiresIn, err := e.fetchOBOToken(ctx, incomingToken)
	if err != nil {
		return "", fmt.Errorf("OBO exchange failed: %w", err)
	}

	// Cache with margin before expiry
	ttl := time.Duration(expiresIn-60) * time.Second
	if ttl > 0 {
		e.tokenCache.Set(cacheKey, token, ttl)
	}

	logger.Debug().Msg("OBO token exchange successful")
	return token, nil
}

func (e *OBOExchanger) fetchOBOToken(ctx context.Context, assertion string) (string, int, error) {
	logger := observability.LoggerFromContext(ctx, *e.logger)
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", e.tenantID)

	data := url.Values{}
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	data.Set("client_id", e.clientID)
	data.Set("client_secret", e.clientSecret)
	data.Set("assertion", assertion)
	data.Set("scope", strings.Join(e.scopes, " "))
	data.Set("requested_token_use", "on_behalf_of")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("failed to create OBO request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("OBO request failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("failed to read OBO response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp tokenEndpointError
		if err := json.Unmarshal(bodyBytes, &errResp); err == nil && errResp.Error != "" {
			logger.Error().
				Int("status_code", resp.StatusCode).
				Str("azure_error", errResp.Error).
				Str("azure_error_description", errResp.ErrorDescription).
				Interface("azure_error_codes", errResp.ErrorCodes).
				Str("azure_trace_id", errResp.TraceID).
				Str("azure_correlation_id", errResp.CorrelationID).
				Str("azure_timestamp", errResp.Timestamp).
				Msg("OBO token endpoint returned error")

			return "", 0, fmt.Errorf(
				"OBO token endpoint returned %d: %s (%s)",
				resp.StatusCode,
				errResp.Error,
				errResp.ErrorDescription,
			)
		}

		logger.Error().
			Int("status_code", resp.StatusCode).
			Str("raw_response", string(bodyBytes)).
			Msg("OBO token endpoint returned non-JSON error")

		return "", 0, fmt.Errorf("OBO token endpoint returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp oboTokenResponse
	if err := json.Unmarshal(bodyBytes, &tokenResp); err != nil {
		return "", 0, fmt.Errorf("failed to decode OBO response: %w", err)
	}

	return tokenResp.AccessToken, tokenResp.ExpiresIn, nil
}

type jwtDebugClaims struct {
	Audience string
	Issuer   string
	TenantID string
	Scope    string
}

func parseJWTDebugClaims(token string) (*jwtDebugClaims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode payload: %w", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}

	claims := &jwtDebugClaims{
		Issuer:   asString(payload["iss"]),
		TenantID: asString(payload["tid"]),
		Scope:    asString(payload["scp"]),
	}
	switch aud := payload["aud"].(type) {
	case string:
		claims.Audience = aud
	case []interface{}:
		if len(aud) > 0 {
			claims.Audience = asString(aud[0])
		}
	}

	return claims, nil
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
