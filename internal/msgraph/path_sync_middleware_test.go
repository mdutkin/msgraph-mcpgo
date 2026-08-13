package msgraph

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	absauth "github.com/microsoft/kiota-abstractions-go/authentication"
	khttp "github.com/microsoft/kiota-http-go"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	msgraphcore "github.com/microsoftgraph/msgraph-sdk-go-core"
)

// TestPathSyncMiddleware_EncodesBase64MessageID is a regression test for the
// "Id is malformed" error returned by Microsoft Graph when fetching email
// details by Base64 message ID.
//
// The Kiota UrlReplaceHandler mutates req.URL.Path to translate the
// "/users/me-token-to-replace" placeholder into "/me", but leaves RawPath
// pointing at the pre-replacement segment. After that mutation, Go's
// url.URL.EscapedPath() falls back to encoding the new Path with mode
// encodePath, which per RFC 3986 leaves "=" unescaped. The request then goes
// out with raw "=" instead of "%3D" and Graph treats the ID as malformed.
// Our pathSyncHandler rewrites RawPath so that EscapedPath() yields a
// Graph-acceptable URI.
func TestPathSyncMiddleware_EncodesBase64MessageID(t *testing.T) {
	const messageID = "AAMkADNmYWQ2N2RjLWJmYTctNDhiYS1hOTk5LWQ1NThmZmQwYzFiMgBGAAAAAADPu8pLHOXgRYAREyjXWjlGBwD10itztqx6Q7pDV9fKvT_VAAAAAAEMAAD10itztqx6Q7pDV9fKvT_VAAGoxXf4AAA="

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, _ := httputil.DumpRequest(r, false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ABC"}`))
		w.Header().Set("X-Captured", string(dump))
	}))
	defer server.Close()

	runAndCapture := func(extraMiddleware ...khttp.Middleware) string {
		captured := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			dump, _ := httputil.DumpRequest(r, false)
			captured <- string(dump)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"ABC"}`))
		}))
		defer srv.Close()

		opts := msgraphcore.GraphClientOptions{}
		mws := msgraphcore.GetDefaultMiddlewaresWithOptions(&opts)
		mws = append(mws, extraMiddleware...)
		httpClient := khttp.GetDefaultClient(mws...)

		adapter, err := msgraphsdk.NewGraphRequestAdapterWithParseNodeFactoryAndSerializationWriterFactoryAndHttpClient(
			&absauth.AnonymousAuthenticationProvider{}, nil, nil, httpClient,
		)
		if err != nil {
			t.Fatalf("adapter: %v", err)
		}
		adapter.SetBaseUrl(srv.URL + "/v1.0")
		client := msgraphsdk.NewGraphServiceClient(adapter)

		if _, err := client.Me().Messages().ByMessageId(messageID).Get(context.Background(), nil); err != nil {
			t.Fatalf("ByMessageId: %v", err)
		}
		return <-captured
	}

	t.Run("without middleware sends raw '=' (the bug)", func(t *testing.T) {
		got := runAndCapture()
		if !strings.Contains(got, "AAA=") {
			t.Fatalf("expected raw '=' in path to demonstrate the bug, got: %s", got)
		}
	})

	t.Run("with pathSyncHandler sends '%3D'", func(t *testing.T) {
		got := runAndCapture(pathSyncHandler{})
		if strings.Contains(got, "AAA=") {
			t.Errorf("request URI still contains raw '=' padding; Graph will reject it.\n%s", got)
		}
		if !strings.Contains(got, "AAA%3D") {
			t.Errorf("request URI does not contain percent-encoded '=' in message ID.\n%s", got)
		}
	})

	_ = server
}

func TestReencodePathSegments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "encodes trailing '=' padding",
			in:   "/v1.0/me/messages/AAA=",
			want: "/v1.0/me/messages/AAA%3D",
		},
		{
			name: "encodes '+' inside segment",
			in:   "/v1.0/me/messages/AA+BB=",
			want: "/v1.0/me/messages/AA%2BBB%3D",
		},
		{
			name: "preserves slashes between segments",
			in:   "/v1.0/me/messages/AAA/attachments/BBB=",
			want: "/v1.0/me/messages/AAA/attachments/BBB%3D",
		},
		{
			name: "leaves unreserved characters untouched",
			in:   "/v1.0/me/messages/AAMk_Y2Jj",
			want: "/v1.0/me/messages/AAMk_Y2Jj",
		},
		{
			name: "empty path",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reencodePathSegments(tt.in); got != tt.want {
				t.Errorf("reencodePathSegments(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
