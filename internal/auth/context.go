package auth

import "context"

// contextKey is an unexported key type. Using a dedicated type, rather than a
// string, keeps the value unreachable from any other package and stops a
// collision with a key set by a dependency. `go vet` flags string keys for
// exactly this reason.
type contextKey int

const tokenContextKey contextKey = iota

// ContextWithToken returns a context carrying the caller's bearer token.
//
// The token travels in the context rather than in a field on the server so
// that one server instance can serve every concurrent user. Nothing about the
// caller is ever stored on the Server struct.
func ContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenContextKey, token)
}

// TokenFromContext returns the caller's bearer token. The second result is
// false when no token is present, which must be treated as an authentication
// failure rather than as an anonymous caller.
func TokenFromContext(ctx context.Context) (string, bool) {
	token, ok := ctx.Value(tokenContextKey).(string)
	if !ok || token == "" {
		return "", false
	}
	return token, true
}
