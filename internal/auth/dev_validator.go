package auth

import (
	"context"
	"fmt"
	"time"

	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// ValidateTokenDev validates a token in development mode with relaxed validation
// This accepts tokens issued for Microsoft Graph (https://graph.microsoft.com)
// and uses them for testing purposes
func (v *TokenValidator) ValidateTokenDev(ctx context.Context, tokenString string) (*Claims, error) {
	v.logger.Warn().Msg("Using development mode token validation - NOT for production!")

	// Check cache first
	cacheKey := fmt.Sprintf("token:dev:%s", tokenString)
	if cached, found := v.claimsCache.Get(cacheKey); found {
		v.logger.Debug().Msg("Token claims retrieved from cache (dev mode)")
		return cached.(*Claims), nil
	}

	// Get JWKS keys
	keySet, err := v.jwksCache.GetKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWKS keys: %w", err)
	}

	// Parse token with relaxed validation
	token, err := jwt.ParseString(
		tokenString,
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
	)
	if err != nil {
		return nil, apperrors.NewTokenValidationError(fmt.Errorf("failed to parse token: %w", err))
	}

	// Extract claims
	claims := &Claims{}

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
	} else if upn, ok := token.Get("upn"); ok {
		claims.Email, _ = upn.(string)
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

	// Relaxed validation for dev mode
	if err := v.validateClaimsDev(claims); err != nil {
		return nil, apperrors.NewTokenValidationError(err)
	}

	// Cache the validated claims
	ttl := claims.ExpiresIn() - 5*time.Minute
	if ttl > 0 {
		v.claimsCache.Set(cacheKey, claims, ttl)
	}

	v.logger.Debug().
		Str("user_id", claims.GetUserID()).
		Str("email", claims.Email).
		Str("audience", claims.Audience).
		Msg("Token validated successfully (dev mode)")

	return claims, nil
}

// validateClaimsDev validates claims with relaxed rules for development
func (v *TokenValidator) validateClaimsDev(claims *Claims) error {
	// Check expiration
	if claims.IsExpired() {
		return fmt.Errorf("token expired at %v", time.Unix(claims.Expiration, 0))
	}

	// Ensure we have a user identifier
	if claims.GetUserID() == "" {
		return fmt.Errorf("missing user identifier (oid or sub)")
	}

	// Validate issuer (EntraID) - accept both v1.0 and v2.0 tokens in dev mode
	expectedIssuerV2 := fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", v.tenantID)
	expectedIssuerV1 := fmt.Sprintf("https://sts.windows.net/%s/", v.tenantID)

	if claims.Issuer != expectedIssuerV2 && claims.Issuer != expectedIssuerV1 {
		return fmt.Errorf("invalid issuer: expected %s or %s, got %s", expectedIssuerV2, expectedIssuerV1, claims.Issuer)
	}

	// In dev mode, accept any audience (don't enforce client ID match)
	v.logger.Debug().
		Str("audience", claims.Audience).
		Str("issuer", claims.Issuer).
		Str("expected_client_id", v.clientID).
		Msg("Accepting token with different audience in dev mode")

	return nil
}
