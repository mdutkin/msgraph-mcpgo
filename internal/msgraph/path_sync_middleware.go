package msgraph

import (
	nethttp "net/http"
	"strings"

	khttp "github.com/microsoft/kiota-http-go"
)

// pathSyncHandler is a middleware that re-encodes the URL path before the
// transport round-trips it to Microsoft Graph.
//
// Background: the Kiota URL template expander (std-uritemplate) correctly
// percent-encodes path-parameter values, and net/http's url.Parse preserves
// the encoded form in RawPath. However, the built-in UrlReplaceHandler (used
// by the Graph SDK to rewrite "/users/me-token-to-replace" -> "/me") mutates
// req.URL.Path without touching RawPath. Once Path diverges from RawPath's
// decoded form, url.URL.EscapedPath() falls back to encoding the current
// Path with mode encodePath, which — per RFC 3986 — leaves "=" unescaped
// because "=" is technically a reserved-but-allowed character in path
// segments. The request then goes out with raw "=" instead of "%3D", and
// Graph treats it as a malformed Base64 identifier.
//
// This handler runs after the URL replace step and rewrites RawPath so that
// EscapedPath() yields a Graph-acceptable URI: "=", "+" and "/" inside path
// segments are escaped, the slash between segments is not.
type pathSyncHandler struct{}

func (pathSyncHandler) Intercept(pipeline khttp.Pipeline, index int, req *nethttp.Request) (*nethttp.Response, error) {
	if req != nil && req.URL != nil && req.URL.Path != "" {
		req.URL.RawPath = reencodePathSegments(req.URL.Path)
	}
	return pipeline.Next(req, index)
}

// reencodePathSegments returns an escaped version of path suitable for use as
// url.URL.RawPath: each segment is escaped so that "=", "+" and "/" become
// percent-encoded, while the slashes that separate segments are preserved.
func reencodePathSegments(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = strings.NewReplacer(
			"=", "%3D",
			"+", "%2B",
		).Replace(seg)
	}
	return strings.Join(segments, "/")
}
