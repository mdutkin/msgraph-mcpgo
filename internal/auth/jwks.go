package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/rs/zerolog"
)

// JWKSCache holds Microsoft Entra's token signing keys.
//
// Entra rotates its signing keys on its own schedule and publishes the current
// set at the tenant's JWKS endpoint. Microsoft's guidance is to re-read that
// set about every 24 hours. This cache re-reads it on a shorter interval and,
// on top of that, re-reads it on demand when a token arrives signed by a key it
// has not seen.
//
// The on-demand path is the one that matters in practice. A purely
// interval-based cache rejects every request signed with a newly rotated key
// until its timer happens to fire, which is an outage of up to a full interval
// caused by a change on Microsoft's side rather than ours.
//
// The fetch is performed directly rather than through jwk.Cache. That cache
// applies its own interval logic to an explicit Refresh call, so a forced
// refresh is silently dropped when the library considers the entry fresh —
// which defeats the whole point of the on-demand path. Owning the fetch means
// the only thing that can suppress it is the rate limit below, which is
// deliberate.
//
// On-demand fetches are rate limited. Without that, an unauthenticated caller
// sending tokens with random key identifiers could turn this service into a
// request amplifier against Entra and exhaust its own throttling budget.
type JWKSCache struct {
	jwksURI string
	client  *http.Client

	refreshInterval    time.Duration
	minRefreshInterval time.Duration

	mu        sync.Mutex
	set       jwk.Set
	fetchedAt time.Time

	// lastOnDemand is tracked separately from fetchedAt because the two serve
	// different budgets. A scheduled or startup fetch must not consume the
	// on-demand allowance: if it did, a key rotation in the first few minutes
	// after a task starts could not be picked up at all, which is exactly the
	// window a deployment is most likely to be in.
	lastOnDemand time.Time

	logger *zerolog.Logger
}

// JWKSConfig configures the key cache.
type JWKSConfig struct {
	// TenantID is the Entra directory whose keys are trusted.
	TenantID string

	// RefreshInterval is how long a fetched key set is served before it is
	// considered stale and re-read.
	RefreshInterval time.Duration

	// MinRefreshInterval is the floor between fetches triggered by an
	// unrecognised key identifier.
	MinRefreshInterval time.Duration

	// HTTPClient is used for discovery and key retrieval.
	HTTPClient *http.Client

	// DiscoveryURL overrides the OpenID configuration URL. A sovereign cloud
	// needs its own authority; the public cloud needs nothing here.
	DiscoveryURL string

	Logger *zerolog.Logger
}

// discoveryDocument is the subset of the OpenID configuration this needs.
type discoveryDocument struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

// NewJWKSCache reads the tenant's OpenID configuration to find the key
// endpoint, then fetches the key set once so that a misconfigured tenant or a
// blocked egress path fails at startup rather than on the first user request.
//
// The endpoint is discovered rather than constructed: a hardcoded URL silently
// becomes wrong when Microsoft changes it, and the discovery document is also
// what names the issuer the tokens must carry.
func NewJWKSCache(ctx context.Context, cfg JWKSConfig) (*JWKSCache, string, error) {
	if cfg.Logger == nil {
		return nil, "", fmt.Errorf("jwks cache: logger is required")
	}
	if cfg.TenantID == "" {
		return nil, "", fmt.Errorf("jwks cache: tenant ID is required")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}

	refreshInterval := cfg.RefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = 12 * time.Hour
	}
	minRefreshInterval := cfg.MinRefreshInterval
	if minRefreshInterval <= 0 {
		minRefreshInterval = 5 * time.Minute
	}

	discoveryURL := cfg.DiscoveryURL
	if discoveryURL == "" {
		discoveryURL = fmt.Sprintf(
			"https://login.microsoftonline.com/%s/v2.0/.well-known/openid-configuration",
			url.PathEscape(cfg.TenantID))
	}

	doc, err := fetchDiscoveryDocument(ctx, httpClient, discoveryURL)
	if err != nil {
		return nil, "", err
	}
	if doc.JWKSURI == "" {
		return nil, "", fmt.Errorf("jwks cache: discovery document at %s names no jwks_uri", discoveryURL)
	}

	c := &JWKSCache{
		jwksURI:            doc.JWKSURI,
		client:             httpClient,
		refreshInterval:    refreshInterval,
		minRefreshInterval: minRefreshInterval,
		logger:             cfg.Logger,
	}

	if err := c.fetch(ctx); err != nil {
		return nil, "", fmt.Errorf("jwks cache: initial fetch: %w", err)
	}

	cfg.Logger.Info().
		Str("jwks_uri", doc.JWKSURI).
		Str("issuer", doc.Issuer).
		Int("keys", c.keyCount()).
		Dur("refresh_interval", refreshInterval).
		Dur("min_refresh_interval", minRefreshInterval).
		Msg("Entra signing keys cached")

	return c, doc.Issuer, nil
}

func fetchDiscoveryDocument(ctx context.Context, client *http.Client, discoveryURL string) (*discoveryDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwks cache: build discovery request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks cache: fetch %s: %w", discoveryURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks cache: %s returned %d", discoveryURL, resp.StatusCode)
	}

	var doc discoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("jwks cache: decode discovery document: %w", err)
	}
	return &doc, nil
}

// fetch reads the key set and replaces the cached copy.
func (c *JWKSCache) fetch(ctx context.Context) error {
	set, err := jwk.Fetch(ctx, c.jwksURI, jwk.WithHTTPClient(c.client))
	if err != nil {
		return fmt.Errorf("fetch %s: %w", c.jwksURI, err)
	}

	c.mu.Lock()
	c.set = set
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

func (c *JWKSCache) keyCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil {
		return 0
	}
	return c.set.Len()
}

// snapshot returns the cached set and whether it is past its refresh interval.
func (c *JWKSCache) snapshot() (jwk.Set, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.set, time.Since(c.fetchedAt) >= c.refreshInterval
}

// KeySet returns the cached key set, re-reading it first when it is stale.
//
// A failed refresh of a stale set is not fatal: the previous keys are still the
// ones Entra published, and rejecting every request because Entra is briefly
// unreachable would be a worse outcome than serving slightly old keys.
func (c *JWKSCache) KeySet(ctx context.Context) (jwk.Set, error) {
	set, stale := c.snapshot()

	if stale {
		if err := c.fetch(ctx); err != nil {
			c.logger.Warn().Err(err).Msg("Scheduled key set refresh failed; continuing with the cached keys")
		} else {
			set, _ = c.snapshot()
		}
	}

	if set == nil {
		return nil, fmt.Errorf("jwks cache: no key set available")
	}
	return set, nil
}

// KeySetForKeyID returns a key set containing kid, re-reading the published
// keys once if the cached set does not have it.
//
// The fetch is skipped when one was attempted within MinRefreshInterval. A
// caller sending unrecognised key identifiers then gets the cached set, fails
// verification, and costs Entra nothing.
func (c *JWKSCache) KeySetForKeyID(ctx context.Context, kid string) (jwk.Set, error) {
	set, err := c.KeySet(ctx)
	if err != nil {
		return nil, err
	}
	if kid == "" {
		return set, nil
	}
	if _, found := set.LookupKeyID(kid); found {
		return set, nil
	}

	if !c.allowOnDemandFetch() {
		c.logger.Debug().
			Str("kid", kid).
			Msg("Unknown signing key, but the published keys were read too recently to read them again")
		return set, nil
	}

	c.logger.Info().
		Str("kid", kid).
		Msg("Token signed by an unknown key; re-reading the Entra key set")

	if err := c.fetch(ctx); err != nil {
		// Fall back to the cached set. A transient failure reaching Entra must
		// not reject a token the cached keys can still verify.
		c.logger.Warn().Err(err).Msg("On-demand key set refresh failed; continuing with the cached keys")
		return set, nil
	}

	refreshed, _ := c.snapshot()
	return refreshed, nil
}

// allowOnDemandFetch reports whether a fetch triggered by an unknown key may
// run now, and records the attempt if so.
//
// The attempt is recorded before the fetch runs and regardless of its outcome,
// so a failing endpoint cannot be hammered either. The budget is independent of
// scheduled refreshes; see the lastOnDemand field.
func (c *JWKSCache) allowOnDemandFetch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastOnDemand.IsZero() && time.Since(c.lastOnDemand) < c.minRefreshInterval {
		return false
	}
	c.lastOnDemand = time.Now()
	return true
}

// expectedIssuers returns the issuer values Entra uses for a tenant, in both
// token versions. v2.0 tokens carry the login.microsoftonline.com form and
// v1.0 tokens the sts.windows.net form, and which one appears depends on the
// registration that issued the token rather than on anything this server
// controls.
func expectedIssuers(tenantID string) []string {
	return []string{
		fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", tenantID),
		fmt.Sprintf("https://sts.windows.net/%s/", tenantID),
	}
}

// isGUID reports whether s has the shape of a directory identifier. Microsoft's
// validation guidance requires the tenant claim to be a GUID before it is used
// as a trust boundary, so a value such as "common" cannot be passed off as one.
func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// normalizeTenant lowercases a directory identifier for comparison.
func normalizeTenant(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
