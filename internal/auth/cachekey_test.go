package auth

import (
	"strings"
	"testing"
)

func TestCacheKeyDoesNotLeakToken(t *testing.T) {
	const token = "eyJhbGciOiJSUzI1NiJ9.payload.signature-material"

	key := cacheKey("obo", token)
	if strings.Contains(key, "signature-material") || strings.Contains(key, token) {
		t.Fatalf("cache key embeds the token: %q", key)
	}
	if !strings.HasPrefix(key, "obo:") {
		t.Fatalf("missing namespace prefix: %q", key)
	}
	if key != cacheKey("obo", token) {
		t.Fatal("cache key is not stable")
	}
}

// Tokens that share a suffix must not share a cache key. An earlier
// implementation keyed on the last twenty characters, so two tokens differing
// only in header or payload collided and one user could be served another
// user's delegated Graph token.
func TestCacheKeySeparatesTokensWithSharedSuffix(t *testing.T) {
	const suffix = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	if cacheKey("obo", "header-a.payload-a."+suffix) == cacheKey("obo", "header-b.payload-b."+suffix) {
		t.Fatal("tokens sharing a suffix produced the same cache key")
	}
}

func TestCacheKeySeparatesNamespaces(t *testing.T) {
	const token = "same.token.value"
	if cacheKey("obo", token) == cacheKey("claims", token) {
		t.Fatal("namespaces do not separate keys")
	}
}

// A narrower scope set must not be served an entry minted for a wider one.
func TestCacheKeySeparatesScopeSets(t *testing.T) {
	const token = "same.token.value"

	narrow := cacheKey("obo", token, scopeDiscriminator([]string{"User.Read"}))
	wide := cacheKey("obo", token, scopeDiscriminator([]string{"User.Read", "Mail.Read"}))
	if narrow == wide {
		t.Fatal("different scope sets produced the same cache key")
	}
}

func TestCacheKeyFieldsAreUnambiguous(t *testing.T) {
	if cacheKey("ns", "ab", "c") == cacheKey("ns", "a", "bc") {
		t.Fatal("field boundaries are ambiguous")
	}
}
