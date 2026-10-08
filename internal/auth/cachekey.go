package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// cacheKey derives a collision-resistant cache key from a bearer token and an
// optional set of discriminators.
//
// A bearer token must never be used as a map key directly, and must never be
// abbreviated into one. Both forms were present in an earlier revision: a
// claims cache keyed on the whole token string, which kept every live
// credential readable in process memory, and an on-behalf-of cache keyed on the
// token's last twenty characters, where a collision would hand one user's
// delegated Microsoft Graph token to a different user.
//
// SHA-256 over the full token removes both problems: the key is fixed width,
// reveals nothing about the credential, and cannot be produced by a different
// token. Discriminators are mixed in so that a cache which depends on request
// parameters, such as the requested Graph scope set, cannot return an entry
// populated for different parameters.
func cacheKey(namespace, token string, discriminators ...string) string {
	h := sha256.New()
	// Length-prefix every field so concatenation stays unambiguous and two
	// different field splits cannot hash to the same value.
	writeField(h, namespace)
	writeField(h, token)
	for _, d := range discriminators {
		writeField(h, d)
	}
	return namespace + ":" + hex.EncodeToString(h.Sum(nil))
}

func writeField(h interface{ Write([]byte) (int, error) }, field string) {
	var lenBuf [8]byte
	n := uint64(len(field))
	for i := range lenBuf {
		lenBuf[i] = byte(n >> (8 * (7 - i)))
	}
	_, _ = h.Write(lenBuf[:])
	_, _ = h.Write([]byte(field))
}

// scopeDiscriminator renders a scope set into a stable discriminator.
func scopeDiscriminator(scopes []string) string {
	return strings.Join(scopes, " ")
}
