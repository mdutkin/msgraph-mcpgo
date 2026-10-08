package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	testClientID = "55555555-5555-5555-5555-555555555555"
	testOID      = "22222222-2222-2222-2222-222222222222"
)

// fakeEntra serves an OpenID discovery document and a JWKS, and signs
// assertions, so the verifier can be exercised end to end without reaching
// Microsoft.
type fakeEntra struct {
	t        *testing.T
	server   *httptest.Server
	tenantID string

	// keys currently published, by key id.
	published atomic.Value // map[string]jwk.Key

	// signers for every key ever created, including unpublished ones.
	signers map[string]*rsa.PrivateKey

	jwksRequests atomic.Int64
}

func newFakeEntra(t *testing.T) *fakeEntra {
	t.Helper()

	f := &fakeEntra{t: t, tenantID: testTenantID, signers: map[string]*rsa.PrivateKey{}}
	f.published.Store(map[string]jwk.Key{})

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", f.tenantID),
			"jwks_uri": f.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		f.jwksRequests.Add(1)

		set := jwk.NewSet()
		for _, key := range f.publishedKeys() {
			_ = set.AddKey(key)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeEntra) publishedKeys() map[string]jwk.Key {
	return f.published.Load().(map[string]jwk.Key)
}

// addKey creates a signing key. When publish is false the public half is not
// served, which models a key the verifier must refuse.
func (f *fakeEntra) addKey(kid string, publish bool) {
	f.t.Helper()

	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		f.t.Fatalf("GenerateKey: %v", err)
	}
	f.signers[kid] = private

	if !publish {
		return
	}

	public, err := jwk.FromRaw(private.Public())
	if err != nil {
		f.t.Fatalf("jwk.FromRaw: %v", err)
	}
	if err := public.Set(jwk.KeyIDKey, kid); err != nil {
		f.t.Fatalf("set kid: %v", err)
	}
	if err := public.Set(jwk.KeyUsageKey, "sig"); err != nil {
		f.t.Fatalf("set use: %v", err)
	}
	// Entra publishes keys without an "alg" field, which is why the verifier
	// infers the algorithm from the key type. Leaving it unset here keeps the
	// fixture faithful to that.

	next := map[string]jwk.Key{}
	for k, v := range f.publishedKeys() {
		next[k] = v
	}
	next[kid] = public
	f.published.Store(next)
}

// assertion builds a signed identity assertion. Claims passed in override the
// defaults, so a test changes only the field it is about.
func (f *fakeEntra) assertion(kid string, overrides map[string]any) string {
	f.t.Helper()

	private, ok := f.signers[kid]
	if !ok {
		f.t.Fatalf("no signer for kid %q", kid)
	}

	token := jwt.New()
	defaults := map[string]any{
		jwt.IssuerKey:     fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", f.tenantID),
		jwt.AudienceKey:   []string{testClientID},
		jwt.SubjectKey:    "subject-value",
		jwt.ExpirationKey: time.Now().Add(time.Hour),
		jwt.IssuedAtKey:   time.Now(),
		"tid":             f.tenantID,
		"oid":             testOID,
		"name":            "An Employee",
		"email":           "employee@example.com",
	}
	for k, v := range defaults {
		if _, overridden := overrides[k]; !overridden {
			if err := token.Set(k, v); err != nil {
				f.t.Fatalf("set %s: %v", k, err)
			}
		}
	}
	for k, v := range overrides {
		if v == nil {
			continue
		}
		if err := token.Set(k, v); err != nil {
			f.t.Fatalf("set %s: %v", k, err)
		}
	}

	key, err := jwk.FromRaw(private)
	if err != nil {
		f.t.Fatalf("jwk.FromRaw: %v", err)
	}
	if err := key.Set(jwk.KeyIDKey, kid); err != nil {
		f.t.Fatalf("set kid: %v", err)
	}

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		f.t.Fatalf("jwt.Sign: %v", err)
	}
	return string(signed)
}

func (f *fakeEntra) verifier(t *testing.T, minRefresh time.Duration) *IdentityVerifier {
	t.Helper()

	cache, _, err := NewJWKSCache(context.Background(), JWKSConfig{
		TenantID:           f.tenantID,
		RefreshInterval:    time.Hour,
		MinRefreshInterval: minRefresh,
		DiscoveryURL:       f.server.URL + "/.well-known/openid-configuration",
		Logger:             testLogger(),
	})
	if err != nil {
		t.Fatalf("NewJWKSCache: %v", err)
	}

	verifier, err := NewIdentityVerifier(IdentityVerifierConfig{
		JWKS:      cache,
		TenantID:  f.tenantID,
		Audiences: []string{testClientID},
		Logger:    testLogger(),
	})
	if err != nil {
		t.Fatalf("NewIdentityVerifier: %v", err)
	}
	return verifier
}

func TestVerifyAcceptsAValidAssertion(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	claims, err := verifier.Verify(context.Background(), entra.assertion("key-1", nil))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.GetUserID() != testOID {
		t.Errorf("user id = %q", claims.GetUserID())
	}
	if claims.Email != "employee@example.com" {
		t.Errorf("email = %q", claims.Email)
	}
	if claims.TenantID != testTenantID {
		t.Errorf("tenant = %q", claims.TenantID)
	}
}

// The v1.0 issuer form must also be accepted: which one appears depends on the
// registration that issued the token.
func TestVerifyAcceptsTheV1IssuerForm(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	assertion := entra.assertion("key-1", map[string]any{
		jwt.IssuerKey: fmt.Sprintf("https://sts.windows.net/%s/", testTenantID),
	})
	if _, err := verifier.Verify(context.Background(), assertion); err != nil {
		t.Fatalf("v1.0 issuer rejected: %v", err)
	}
}

// An assertion signed by a key Entra does not publish must be refused. This is
// the case a forged assertion falls into.
func TestVerifyRejectsAnUnpublishedKey(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	entra.addKey("forged", false)
	verifier := entra.verifier(t, time.Minute)

	_, err := verifier.Verify(context.Background(), entra.assertion("forged", nil))
	if err == nil {
		t.Fatal("an assertion signed by an unpublished key was accepted")
	}
}

// A rotation must be picked up on demand rather than at the next interval. A
// cache that waited would reject every request until its timer fired.
func TestVerifyPicksUpARotatedKeyImmediately(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-old", true)

	// A production-shaped floor, not a nanosecond. An earlier version of this
	// test used a nanosecond and passed while the live service rejected every
	// token signed with a rotated key, because the forced refresh was being
	// suppressed by the key library's own interval logic rather than by this
	// floor. The floor must be long enough that a suppressed fetch fails the
	// test.
	verifier := entra.verifier(t, 5*time.Minute)

	if _, err := verifier.Verify(context.Background(), entra.assertion("key-old", nil)); err != nil {
		t.Fatalf("baseline verification failed: %v", err)
	}

	before := entra.jwksRequests.Load()
	entra.addKey("key-new", true)

	if _, err := verifier.Verify(context.Background(), entra.assertion("key-new", nil)); err != nil {
		t.Fatalf("a token signed with a newly rotated key was rejected: %v", err)
	}
	if entra.jwksRequests.Load() <= before {
		t.Fatal("the key set was not re-read when an unknown key id arrived")
	}

	// The rotated key must now be served from cache, not re-fetched per call.
	afterFirst := entra.jwksRequests.Load()
	for i := 0; i < 5; i++ {
		if _, err := verifier.Verify(context.Background(), entra.assertion("key-new", nil)); err != nil {
			t.Fatalf("repeat verification with the rotated key failed: %v", err)
		}
	}
	if entra.jwksRequests.Load() != afterFirst {
		t.Error("the key set was re-read for a key already in the cache")
	}
}

// A stale key set is re-read on the next request without needing an unknown
// key id to trigger it.
func TestStaleKeySetIsRefreshedOnAccess(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)

	cache, _, err := NewJWKSCache(context.Background(), JWKSConfig{
		TenantID: testTenantID,
		// Immediately stale, so the next access must re-read.
		RefreshInterval:    time.Nanosecond,
		MinRefreshInterval: time.Hour,
		DiscoveryURL:       entra.server.URL + "/.well-known/openid-configuration",
		Logger:             testLogger(),
	})
	if err != nil {
		t.Fatalf("NewJWKSCache: %v", err)
	}

	before := entra.jwksRequests.Load()
	if _, err := cache.KeySet(context.Background()); err != nil {
		t.Fatalf("KeySet: %v", err)
	}
	if entra.jwksRequests.Load() <= before {
		t.Error("a stale key set was not re-read")
	}
}

// Entra being briefly unreachable must not reject tokens the cached keys can
// still verify.
func TestCachedKeysSurviveAFailingEndpoint(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	assertion := entra.assertion("key-1", nil)
	verifier := entra.verifier(t, time.Minute)

	// Take Entra away after the keys have been cached.
	entra.server.Close()

	if _, err := verifier.Verify(context.Background(), assertion); err != nil {
		t.Fatalf("a verifiable token was rejected while Entra was unreachable: %v", err)
	}
}

// An unknown key id must not become a way to drive traffic at Entra.
func TestUnknownKeyRefreshIsRateLimited(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	entra.addKey("unknown", false)
	verifier := entra.verifier(t, time.Hour)

	if _, err := verifier.Verify(context.Background(), entra.assertion("key-1", nil)); err != nil {
		t.Fatalf("baseline verification failed: %v", err)
	}

	before := entra.jwksRequests.Load()
	for i := 0; i < 20; i++ {
		_, _ = verifier.Verify(context.Background(), entra.assertion("unknown", nil))
	}

	if fetched := entra.jwksRequests.Load() - before; fetched > 1 {
		t.Errorf("20 unknown key ids caused %d key set fetches, expected at most 1", fetched)
	}
}

func TestVerifyRejectsBadClaims(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	tests := map[string]map[string]any{
		"wrong audience": {jwt.AudienceKey: []string{"99999999-9999-9999-9999-999999999999"}},
		"foreign tenant": {"tid": "99999999-9999-9999-9999-999999999999"},
		"tenant is not a guid": {
			"tid":         "common",
			jwt.IssuerKey: "https://login.microsoftonline.com/common/v2.0",
		},
		"issuer from another tenant": {
			jwt.IssuerKey: "https://login.microsoftonline.com/99999999-9999-9999-9999-999999999999/v2.0",
		},
		"issuer is not entra": {jwt.IssuerKey: "https://evil.example.com/" + testTenantID + "/v2.0"},
		"expired":             {jwt.ExpirationKey: time.Now().Add(-time.Hour)},
		"not yet valid":       {jwt.NotBeforeKey: time.Now().Add(time.Hour)},
	}

	for name, overrides := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), entra.assertion("key-1", overrides)); err == nil {
				t.Fatal("a bad assertion was accepted")
			}
		})
	}
}

// Without a subject the request cannot be attributed, so it is refused even
// though the signature is good.
func TestVerifyRejectsAnAssertionWithNoSubject(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	token := jwt.New()
	_ = token.Set(jwt.IssuerKey, fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", testTenantID))
	_ = token.Set(jwt.AudienceKey, []string{testClientID})
	_ = token.Set(jwt.ExpirationKey, time.Now().Add(time.Hour))
	_ = token.Set("tid", testTenantID)

	key, _ := jwk.FromRaw(entra.signers["key-1"])
	_ = key.Set(jwk.KeyIDKey, "key-1")
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256, key))
	if err != nil {
		t.Fatalf("jwt.Sign: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), string(signed)); err == nil {
		t.Fatal("an assertion with neither oid nor sub was accepted")
	}
}

// The classic algorithm-confusion attack: present the RSA public key as an
// HMAC secret so the attacker can sign their own token with public material.
func TestVerifyRejectsSymmetricAlgorithmConfusion(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	token := jwt.New()
	_ = token.Set(jwt.IssuerKey, fmt.Sprintf("https://login.microsoftonline.com/%s/v2.0", testTenantID))
	_ = token.Set(jwt.AudienceKey, []string{testClientID})
	_ = token.Set(jwt.ExpirationKey, time.Now().Add(time.Hour))
	_ = token.Set("tid", testTenantID)
	_ = token.Set("oid", testOID)

	// The public modulus, which anyone can read from the JWKS, used as an
	// HMAC key.
	secret := entra.signers["key-1"].PublicKey.N.Bytes()
	hmacKey, err := jwk.FromRaw(secret)
	if err != nil {
		t.Fatalf("jwk.FromRaw: %v", err)
	}
	_ = hmacKey.Set(jwk.KeyIDKey, "key-1")

	signed, err := jwt.Sign(token, jwt.WithKey(jwa.HS256, hmacKey))
	if err != nil {
		t.Fatalf("jwt.Sign: %v", err)
	}

	if _, err := verifier.Verify(context.Background(), string(signed)); err == nil {
		t.Fatal("an HMAC-signed assertion was accepted against an RSA key")
	}
}

func TestVerifyRejectsUnsignedAndMalformedAssertions(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)
	verifier := entra.verifier(t, time.Minute)

	// alg: none, with a well-formed payload and an empty signature.
	unsigned := makeToken(t, graphPayload())
	parts := strings.Split(unsigned, ".")
	none := strings.Join([]string{
		"eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0", // {"alg":"none","typ":"JWT"}
		parts[1],
		"",
	}, ".")

	tests := map[string]string{
		"alg none":     none,
		"not a jwt":    "opaque",
		"two parts":    "header.payload",
		"empty":        "",
		"bad base64":   "!!!.!!!.!!!",
		"random bytes": strings.Repeat("a", 64),
	}

	for name, assertion := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(context.Background(), assertion); err == nil {
				t.Fatal("a malformed assertion was accepted")
			}
		})
	}
}

func TestNewIdentityVerifierRequiresConfiguration(t *testing.T) {
	entra := newFakeEntra(t)
	entra.addKey("key-1", true)

	cache, _, err := NewJWKSCache(context.Background(), JWKSConfig{
		TenantID:     testTenantID,
		DiscoveryURL: entra.server.URL + "/.well-known/openid-configuration",
		Logger:       testLogger(),
	})
	if err != nil {
		t.Fatalf("NewJWKSCache: %v", err)
	}

	base := IdentityVerifierConfig{JWKS: cache, TenantID: testTenantID, Audiences: []string{testClientID}, Logger: testLogger()}

	noAudience := base
	noAudience.Audiences = nil
	if _, err := NewIdentityVerifier(noAudience); err == nil {
		t.Error("a verifier with no accepted audience was created")
	}

	badTenant := base
	badTenant.TenantID = "common"
	if _, err := NewIdentityVerifier(badTenant); err == nil {
		t.Error("a verifier with a non-GUID tenant was created")
	}

	noJWKS := base
	noJWKS.JWKS = nil
	if _, err := NewIdentityVerifier(noJWKS); err == nil {
		t.Error("a verifier with no key source was created")
	}
}

// A blocked or misconfigured discovery endpoint must fail at startup, not on
// the first user request.
func TestNewJWKSCacheFailsFastOnABadEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	_, _, err := NewJWKSCache(context.Background(), JWKSConfig{
		TenantID:     testTenantID,
		DiscoveryURL: server.URL + "/.well-known/openid-configuration",
		Logger:       testLogger(),
	})
	if err == nil {
		t.Fatal("a failing discovery endpoint did not stop startup")
	}
}

func TestIsGUID(t *testing.T) {
	valid := []string{testTenantID, "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}
	invalid := []string{"", "common", "organizations", testTenantID + "x", strings.ReplaceAll(testTenantID, "-", ""), "zzzzzzzz-1111-1111-1111-111111111111"}

	for _, s := range valid {
		if !isGUID(s) {
			t.Errorf("%q should be a GUID", s)
		}
	}
	for _, s := range invalid {
		if isGUID(s) {
			t.Errorf("%q should not be a GUID", s)
		}
	}
}
