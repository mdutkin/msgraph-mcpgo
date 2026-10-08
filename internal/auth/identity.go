package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/rs/zerolog"
)

// acceptedSigningAlgorithms are the algorithms an identity assertion may be
// signed with.
//
// The list is fixed here rather than taken from the token. A verifier that
// honours the header's own "alg" can be told to accept "none", or to treat an
// RSA public key as an HMAC secret, and in both cases an attacker signs their
// own assertion. Entra signs with RS256; the other two are listed only so that
// a future change of Microsoft's algorithm does not require a code change to
// recover.
var acceptedSigningAlgorithms = []jwa.SignatureAlgorithm{
	jwa.RS256,
	jwa.RS384,
	jwa.RS512,
}

// clockSkew tolerated when checking expiry and not-before.
const clockSkew = 2 * time.Minute

// IdentityVerifier cryptographically verifies an identity assertion issued by
// Microsoft Entra.
//
// This is the part of the request that can be verified. The Microsoft Graph
// token in the Authorization header cannot be: Microsoft does not publish
// signing keys for tokens issued to its own APIs, and documents that a
// resource must only validate tokens whose audience is itself ("you can't
// validate tokens for Microsoft Graph according to these rules due to their
// proprietary format"). The assertion, by contrast, is issued for a registered
// application, so its signature verifies against the tenant's published keys.
//
// Verifying it establishes who the caller is. It does not establish what they
// may do: that is still decided by the delegated permissions inside the
// forwarded Graph token and enforced by Microsoft Graph.
type IdentityVerifier struct {
	jwks      *JWKSCache
	tenantID  string
	audiences []string
	issuers   []string
	logger    *zerolog.Logger
}

// IdentityVerifierConfig configures the verifier.
type IdentityVerifierConfig struct {
	// JWKS supplies Entra's signing keys.
	JWKS *JWKSCache

	// TenantID is the only directory whose assertions are accepted.
	TenantID string

	// Audiences are the accepted "aud" values, each naming an application the
	// assertion may have been issued for.
	//
	// For an assertion minted for the calling application, such as LibreChat's
	// OpenID ID token, this is that application's client ID. For an assertion
	// minted for this server, it is this server's own identifier, for example
	// api://<client-id>. Both work; the difference is which registration issues
	// it, not how it is verified.
	Audiences []string

	// AdditionalIssuers extends the accepted issuer list beyond the two forms
	// Entra uses for the configured tenant. Normally empty.
	AdditionalIssuers []string

	Logger *zerolog.Logger
}

// NewIdentityVerifier builds a verifier.
func NewIdentityVerifier(cfg IdentityVerifierConfig) (*IdentityVerifier, error) {
	if cfg.Logger == nil {
		return nil, fmt.Errorf("identity verifier: logger is required")
	}
	if cfg.JWKS == nil {
		return nil, fmt.Errorf("identity verifier: a JWKS cache is required")
	}
	if !isGUID(cfg.TenantID) {
		return nil, fmt.Errorf("identity verifier: tenant ID %q is not a GUID", cfg.TenantID)
	}
	if len(cfg.Audiences) == 0 {
		return nil, fmt.Errorf(
			"identity verifier: at least one accepted audience is required, otherwise any " +
				"assertion the tenant ever issued would be accepted here")
	}

	return &IdentityVerifier{
		jwks:      cfg.JWKS,
		tenantID:  normalizeTenant(cfg.TenantID),
		audiences: cfg.Audiences,
		issuers:   append(expectedIssuers(cfg.TenantID), cfg.AdditionalIssuers...),
		logger:    cfg.Logger,
	}, nil
}

// Verify checks an assertion's signature and claims, and returns the verified
// identity.
//
// Unlike the claims read from the Graph token, everything returned here is
// backed by Entra's signature.
func (v *IdentityVerifier) Verify(ctx context.Context, assertion string) (*Claims, error) {
	kid, err := signingKeyID(assertion)
	if err != nil {
		return nil, err
	}

	// Looking the key up by its identifier is what lets a rotation be handled
	// by re-reading the key set instead of failing until a timer fires.
	keySet, err := v.jwks.KeySetForKeyID(ctx, kid)
	if err != nil {
		return nil, err
	}

	token, err := jwt.ParseString(assertion,
		jwt.WithKeySet(keySet,
			// Entra publishes keys without an "alg" field, so the algorithm is
			// inferred from the key type rather than read from the token. The
			// inference is over key material, so it cannot be steered by the
			// header into accepting "none".
			jws.WithInferAlgorithmFromKey(true),
			jws.WithUseDefault(false),
		),
		jwt.WithValidate(true),
		jwt.WithAcceptableSkew(clockSkew),
	)
	if err != nil {
		return nil, fmt.Errorf("assertion signature or lifetime is invalid: %w", err)
	}

	if err := v.checkAlgorithm(assertion); err != nil {
		return nil, err
	}

	claims, err := claimsFromVerifiedToken(token)
	if err != nil {
		return nil, err
	}

	if err := v.checkIssuerAndTenant(claims); err != nil {
		return nil, err
	}
	if err := v.checkAudience(token); err != nil {
		return nil, err
	}
	if claims.GetUserID() == "" {
		return nil, fmt.Errorf("assertion carries no user identifier (oid or sub)")
	}

	return claims, nil
}

// checkAlgorithm rejects a signature algorithm outside the accepted list.
func (v *IdentityVerifier) checkAlgorithm(assertion string) error {
	msg, err := jws.ParseString(assertion)
	if err != nil {
		return fmt.Errorf("assertion is not a signed JWT: %w", err)
	}

	signatures := msg.Signatures()
	if len(signatures) == 0 {
		return fmt.Errorf("assertion carries no signature")
	}

	for _, sig := range signatures {
		alg := sig.ProtectedHeaders().Algorithm()
		if !slices.Contains(acceptedSigningAlgorithms, alg) {
			return fmt.Errorf("assertion signature algorithm %q is not accepted", alg)
		}
	}
	return nil
}

// checkIssuerAndTenant ties the token to the configured directory.
//
// Microsoft's guidance is to confirm that the tenant claim is a GUID, that the
// issuer names that same tenant, and that the issuer is one the application
// expects. Checking the issuer alone is not enough: the issuer is a template
// parameterised by tenant, so a token from any directory satisfies its shape.
func (v *IdentityVerifier) checkIssuerAndTenant(claims *Claims) error {
	if !isGUID(claims.TenantID) {
		return fmt.Errorf("assertion tenant claim %q is not a GUID", claims.TenantID)
	}
	if normalizeTenant(claims.TenantID) != v.tenantID {
		return fmt.Errorf("assertion is from directory %q, not the configured one", claims.TenantID)
	}

	for _, issuer := range v.issuers {
		if strings.EqualFold(claims.Issuer, issuer) {
			return nil
		}
	}
	return fmt.Errorf("assertion issuer %q is not an Entra issuer for the configured tenant", claims.Issuer)
}

// checkAudience requires the assertion to name an application this server
// accepts assertions for.
func (v *IdentityVerifier) checkAudience(token jwt.Token) error {
	for _, got := range token.Audience() {
		if slices.Contains(v.audiences, got) {
			return nil
		}
	}
	return fmt.Errorf("assertion audience %v is not accepted here", token.Audience())
}

// claimsFromVerifiedToken copies the claims this server uses off a token whose
// signature has already been checked.
func claimsFromVerifiedToken(token jwt.Token) (*Claims, error) {
	claims := &Claims{
		Subject: token.Subject(),
		Issuer:  token.Issuer(),
	}
	if aud := token.Audience(); len(aud) > 0 {
		claims.Audience = aud[0]
	}
	if exp := token.Expiration(); !exp.IsZero() {
		claims.Expiration = exp.Unix()
	}

	stringClaim := func(key string) string {
		if raw, ok := token.Get(key); ok {
			if s, ok := raw.(string); ok {
				return s
			}
		}
		return ""
	}

	claims.ObjectID = stringClaim("oid")
	claims.TenantID = stringClaim("tid")
	claims.Name = stringClaim("name")

	claims.Email = stringClaim("email")
	if claims.Email == "" {
		claims.Email = stringClaim("preferred_username")
	}
	if claims.Email == "" {
		claims.Email = stringClaim("upn")
	}

	return claims, nil
}

// signingKeyID reads the key identifier from a JWT header without verifying
// anything. The value selects which published key to check the signature
// against, so a wrong or absent one costs a failed verification and nothing
// more.
func signingKeyID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("assertion is not a JWT")
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("assertion header is not valid base64url: %w", err)
	}

	var header struct {
		KeyID     string `json:"kid"`
		Algorithm string `json:"alg"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return "", fmt.Errorf("assertion header is not valid JSON: %w", err)
	}
	if strings.EqualFold(header.Algorithm, "none") {
		return "", fmt.Errorf("assertion is unsigned")
	}
	return header.KeyID, nil
}
