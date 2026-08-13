package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	cache "github.com/patrickmn/go-cache"
	"github.com/rs/zerolog"
)

const (
	jwksCacheKey = "jwks_keys"
)

// JWKSCache caches JWKS keys from EntraID
type JWKSCache struct {
	tenantID string
	cache    *cache.Cache
	logger   *zerolog.Logger
	ttl      time.Duration
}

// NewJWKSCache creates a new JWKS cache
func NewJWKSCache(tenantID string, ttl time.Duration, logger *zerolog.Logger) *JWKSCache {
	return &JWKSCache{
		tenantID: tenantID,
		cache:    cache.New(ttl, ttl*2),
		logger:   logger,
		ttl:      ttl,
	}
}

// GetKeys returns the JWKS keys, fetching from EntraID if not cached
func (j *JWKSCache) GetKeys(ctx context.Context) (jwk.Set, error) {
	// Check cache first
	if cached, found := j.cache.Get(jwksCacheKey); found {
		j.logger.Debug().Msg("JWKS keys retrieved from cache")
		return cached.(jwk.Set), nil
	}

	// Fetch from EntraID
	j.logger.Info().Msg("Fetching JWKS keys from EntraID")
	keys, err := j.fetchKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS keys: %w", err)
	}

	// Cache the keys
	j.cache.Set(jwksCacheKey, keys, j.ttl)

	return keys, nil
}

// fetchKeys fetches JWKS keys from both v1.0 and v2.0 EntraID endpoints
func (j *JWKSCache) fetchKeys(ctx context.Context) (jwk.Set, error) {
	// Create a new keyset to hold all keys
	combinedSet := jwk.NewSet()

	// Fetch v2.0 keys (for v2.0 tokens)
	jwksURLV2 := fmt.Sprintf("https://login.microsoftonline.com/%s/discovery/v2.0/keys", j.tenantID)
	setV2, err := jwk.Fetch(ctx, jwksURLV2)
	if err != nil {
		j.logger.Warn().Err(err).Str("url", jwksURLV2).Msg("Failed to fetch v2.0 JWKS keys")
	} else {
		// Add v2.0 keys to combined set
		for i := 0; i < setV2.Len(); i++ {
			key, _ := setV2.Key(i)
			combinedSet.AddKey(key)
		}
		j.logger.Debug().Int("v2_keys", setV2.Len()).Msg("Fetched v2.0 JWKS keys")
	}

	// Fetch v1.0 keys (for v1.0 tokens)
	jwksURLV1 := fmt.Sprintf("https://login.microsoftonline.com/%s/discovery/keys", j.tenantID)
	setV1, err := jwk.Fetch(ctx, jwksURLV1)
	if err != nil {
		j.logger.Warn().Err(err).Str("url", jwksURLV1).Msg("Failed to fetch v1.0 JWKS keys")
	} else {
		// Add v1.0 keys to combined set
		for i := 0; i < setV1.Len(); i++ {
			key, _ := setV1.Key(i)
			combinedSet.AddKey(key)
		}
		j.logger.Debug().Int("v1_keys", setV1.Len()).Msg("Fetched v1.0 JWKS keys")
	}

	if combinedSet.Len() == 0 {
		return nil, fmt.Errorf("failed to fetch any JWKS keys from v1.0 or v2.0 endpoints")
	}

	j.logger.Info().
		Int("total_key_count", combinedSet.Len()).
		Msg("Successfully fetched JWKS keys from v1.0 and v2.0 endpoints")

	return combinedSet, nil
}

// Refresh explicitly refreshes the JWKS keys
func (j *JWKSCache) Refresh(ctx context.Context) error {
	keys, err := j.fetchKeys(ctx)
	if err != nil {
		return err
	}

	j.cache.Set(jwksCacheKey, keys, j.ttl)
	return nil
}

// Clear clears the JWKS cache
func (j *JWKSCache) Clear() {
	j.cache.Delete(jwksCacheKey)
	j.logger.Info().Msg("JWKS cache cleared")
}
