package observability

import (
	"crypto/sha256"
	"encoding/hex"
)

// redactDigestLen is the number of hex characters kept from the SHA-256 sum.
// Twelve characters (48 bits) make accidental collisions negligible for log
// correlation while keeping log lines short.
const redactDigestLen = 12

// Redact converts a user-content string into a short, stable digest that is
// safe to write to a log sink.
//
// Mailbox subjects, search queries and OData filters are customer content.
// They must not appear in CloudWatch, in a log aggregator, or in any support
// bundle. A digest still lets an operator correlate repeated values across
// log lines ("this same query failed four times") and compare a log entry
// against a value supplied during an incident, without disclosing the value
// itself.
//
// An empty input returns an empty string so that an absent field stays
// visibly absent rather than becoming the digest of "".
func Redact(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])[:redactDigestLen]
}

// RedactAll applies Redact to every element of values.
func RedactAll(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = Redact(v)
	}
	return out
}
