package auth

import (
	"context"
	"fmt"
	"time"

	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
	cache "github.com/patrickmn/go-cache"
	"github.com/rs/zerolog"
)

// TokenValidator validates JWT tokens from EntraID
type TokenValidator struct {
	clientID    string
	tenantID    string
	jwksCache   *JWKSCache
	claimsCache *cache.Cache
	logger      *zerolog.Logger
	metrics     *observability.Metrics
}

// NewTokenValidator creates a new token validator
func NewTokenValidator(
	clientID string,
	tenantID string,
	jwksCache *JWKSCache,
	logger *zerolog.Logger,
	metrics *observability.Metrics,
) *TokenValidator {
	return &TokenValidator{
		clientID:    clientID,
		tenantID:    tenantID,
		jwksCache:   jwksCache,
		claimsCache: cache.New(5*time.Minute, 10*time.Minute),
		logger:      logger,
		metrics:     metrics,
	}
}

// ValidateToken validates a JWT token and returns the claims
func (v *TokenValidator) ValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		v.metrics.TokenValidationLatency.WithLabelValues("total").Observe(duration)
	}()

	// Check cache first
	cacheKey := fmt.Sprintf("token:%s", tokenString)
	if cached, found := v.claimsCache.Get(cacheKey); found {
		v.logger.Debug().Msg("Token claims retrieved from cache")
		v.metrics.TokenValidationTotal.WithLabelValues("success_cached").Inc()
		v.metrics.CacheHits.WithLabelValues("token_validation").Inc()
		v.metrics.TokenValidationLatency.WithLabelValues("cached").Observe(time.Since(start).Seconds())
		return cached.(*Claims), nil
	}

	v.metrics.CacheMisses.WithLabelValues("token_validation").Inc()

	// Parse and validate token
	claims, err := v.parseAndValidateToken(ctx, tokenString)
	if err != nil {
		v.metrics.TokenValidationTotal.WithLabelValues("failure").Inc()
		v.metrics.TokenValidationErrors.WithLabelValues("validation_failed").Inc()
		v.logger.Error().Err(err).Msg("Token validation failed")
		return nil, apperrors.NewTokenValidationError(err)
	}

	// Cache the validated claims
	// TTL = token expiration - 5 minutes (safety margin)
	ttl := claims.ExpiresIn() - 5*time.Minute
	if ttl > 0 {
		v.claimsCache.Set(cacheKey, claims, ttl)
		v.logger.Debug().
			Dur("ttl", ttl).
			Msg("Token claims cached")
	}

	v.metrics.TokenValidationTotal.WithLabelValues("success").Inc()
	v.metrics.TokenValidationLatency.WithLabelValues("validated").Observe(time.Since(start).Seconds())

	return claims, nil
}

// parseAndValidateToken parses and validates the JWT token
func (v *TokenValidator) parseAndValidateToken(ctx context.Context, tokenString string) (*Claims, error) {
	// Get JWKS keys
	keySet, err := v.jwksCache.GetKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWKS keys: %w", err)
	}

	// Audience and issuer are the two claims an operator needs when an app
	// registration is misconfigured. They are emitted at debug level only:
	// dumping the whole decoded payload at info level put user identifiers
	// and group claims into the log sink on every request.
	if v.logger.GetLevel() <= zerolog.DebugLevel {
		if parts := strings.SplitN(tokenString, ".", 3); len(parts) >= 2 {
			var payload map[string]interface{}
			if pb, e := base64.RawURLEncoding.DecodeString(parts[1]); e == nil {
				_ = json.Unmarshal(pb, &payload)
			}
			v.logger.Debug().
				Str("aud", fmt.Sprintf("%v", payload["aud"])).
				Str("iss", fmt.Sprintf("%v", payload["iss"])).
				Msg("Token header inspection")
		}
	}

	// Parse token (InferAlgorithmFromKey is needed because Azure JWKS keys
	// often omit the "alg" field, so the library must infer it from the key type)
	token, err := jwt.ParseString(
		tokenString,
		jwt.WithKeySet(keySet, jws.WithInferAlgorithmFromKey(true)),
		jwt.WithValidate(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// Extract claims
	claims := &Claims{}

	// Standard claims
	if sub := token.Subject(); sub != "" {
		claims.Subject = sub
	}

	if aud := token.Audience(); len(aud) > 0 {
		claims.Audience = aud[0]
	}

	if iss := token.Issuer(); iss != "" {
		claims.Issuer = iss
	}

	if exp := token.Expiration(); !exp.IsZero() {
		claims.Expiration = exp.Unix()
	}

	// Custom claims
	if email, ok := token.Get("email"); ok {
		claims.Email, _ = email.(string)
	}

	if name, ok := token.Get("name"); ok {
		claims.Name, _ = name.(string)
	}

	if oid, ok := token.Get("oid"); ok {
		claims.ObjectID, _ = oid.(string)
	}

	if tid, ok := token.Get("tid"); ok {
		claims.TenantID, _ = tid.(string)
	}

	// Validate required claims
	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	v.logger.Debug().
		Str("user_id", claims.GetUserID()).
		Str("email", claims.Email).
		Time("expiration", time.Unix(claims.Expiration, 0)).
		Msg("Token validated successfully")

	return claims, nil
}

// validateClaims validates the token claims
func (v *TokenValidator) validateClaims(claims *Claims) error {
	// Validate audience (client ID or api:// URI)
	expectedAudURI := fmt.Sprintf("api://%s", v.clientID)
	if claims.Audience != v.clientID && claims.Audience != expectedAudURI {
		return fmt.Errorf("invalid audience: expected %s or %s, got %s", v.clientID, expectedAudURI, claims.Audience)
	}

	// Validate issuer (EntraID v1.0 or v2.0)
	expectedIssuerV2 := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", v.tenantID)
	expectedIssuerV1 := fmt.Sprintf("https://sts.windows.net/%s/", v.tenantID)
	if claims.Issuer != expectedIssuerV2 && claims.Issuer != expectedIssuerV1 {
		return fmt.Errorf("invalid issuer: expected %s or %s, got %s", expectedIssuerV2, expectedIssuerV1, claims.Issuer)
	}

	// Check expiration
	if claims.IsExpired() {
		return fmt.Errorf("token expired at %v", time.Unix(claims.Expiration, 0))
	}

	// Ensure we have a user identifier
	if claims.GetUserID() == "" {
		return fmt.Errorf("missing user identifier (oid or sub)")
	}

	return nil
}

// ExtractUserID extracts the user ID from claims
func (v *TokenValidator) ExtractUserID(claims *Claims) string {
	return claims.GetUserID()
}

// ParseClaimsWithoutVerification decodes the JWT payload without verifying the
// signature. Used when SKIP_TOKEN_VALIDATION is true: the token is still
// validated by MS Graph during OBO exchange. Only use for extracting claims
// for logging/context; do not use for authorization decisions.
func (v *TokenValidator) ParseClaimsWithoutVerification(tokenString string) (*Claims, error) {
	parts := strings.SplitN(tokenString, ".", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse JWT payload: %w", err)
	}
	claims := &Claims{}
	if s, ok := raw["sub"].(string); ok {
		claims.Subject = s
	}
	if aud, ok := raw["aud"]; ok {
		if s, ok := aud.(string); ok {
			claims.Audience = s
		} else if arr, ok := aud.([]interface{}); ok && len(arr) > 0 {
			if s, ok := arr[0].(string); ok {
				claims.Audience = s
			}
		}
	}
	if s, ok := raw["iss"].(string); ok {
		claims.Issuer = s
	}
	if exp, ok := raw["exp"]; ok {
		switch t := exp.(type) {
		case float64:
			claims.Expiration = int64(t)
		case int64:
			claims.Expiration = t
		}
	}
	if s, ok := raw["email"].(string); ok {
		claims.Email = s
	}
	if s, ok := raw["name"].(string); ok {
		claims.Name = s
	}
	if s, ok := raw["oid"].(string); ok {
		claims.ObjectID = s
	}
	if s, ok := raw["tid"].(string); ok {
		claims.TenantID = s
	}
	return claims, nil
}
