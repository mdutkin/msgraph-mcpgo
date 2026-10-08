package auth

import "strings"

import "testing"

func TestCacheKeyDoesNotLeakToken(t *testing.T) {
	const token = "eyJhbGciOiJSUzI1NiJ9.payload.signature-material"

	key := cacheKey("token", token)
	if strings.Contains(key, "signature-material") || strings.Contains(key, token) {
		t.Fatalf("cache key embeds the token: %q", key)
	}
	if !strings.HasPrefix(key, "token:") {
		t.Fatalf("missing namespace prefix: %q", key)
	}
	if key != cacheKey("token", token) {
		t.Fatal("cache key is not stable")
	}
}

// Tokens that share a suffix must not share a cache key. The previous OBO
// implementation keyed on the last twenty characters, so two tokens differing
// only in their header or payload collided and one user could be served
// another user's delegated Graph token.
func TestCacheKeySeparatesTokensWithSharedSuffix(t *testing.T) {
	const suffix = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	a := cacheKey("obo", "header-a.payload-a."+suffix)
	b := cacheKey("obo", "header-b.payload-b."+suffix)
	if a == b {
		t.Fatal("tokens sharing a suffix produced the same cache key")
	}
}

func TestCacheKeySeparatesNamespaces(t *testing.T) {
	const token = "same.token.value"
	if cacheKey("token", token) == cacheKey("obo", token) {
		t.Fatal("namespaces do not separate keys")
	}
}

func TestCacheKeySeparatesDiscriminators(t *testing.T) {
	const token = "same.token.value"

	narrow := cacheKey("obo", token, scopeDiscriminator([]string{"User.Read"}))
	wide := cacheKey("obo", token, scopeDiscriminator([]string{"User.Read", "Mail.Read"}))
	if narrow == wide {
		t.Fatal("different scope sets produced the same cache key")
	}
}

// Length prefixing must stop adjacent fields from being re-split.
func TestCacheKeyFieldsAreUnambiguous(t *testing.T) {
	if cacheKey("ns", "ab", "c") == cacheKey("ns", "a", "bc") {
		t.Fatal("field boundaries are ambiguous")
	}
}
