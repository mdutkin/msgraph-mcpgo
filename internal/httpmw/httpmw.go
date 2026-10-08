// Package httpmw holds transport-level middleware that is independent of MCP
// semantics.
package httpmw

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"
)

// DefaultMaxRequestBytes bounds an MCP request body.
//
// The largest legitimate request is an upload_file call, whose content arrives
// base64 encoded and so is about a third larger than the file. 32 MiB admits a
// file of roughly 24 MiB, which is comfortably above anything an assistant
// sends in practice while staying far below the task memory limit.
const DefaultMaxRequestBytes int64 = 32 << 20

// LimitRequestBody rejects a request whose body exceeds maxBytes.
//
// Without a limit the MCP transport calls io.ReadAll on the body, so a single
// request can allocate as much memory as the sender is willing to transmit.
// One authenticated client could exhaust a task's memory and have it killed by
// the container runtime, taking every other user's in-flight request with it.
// A load balancer idle timeout does not help, because a slow sender that keeps
// transmitting is not idle.
//
// The limit is applied before the transport reads anything, and
// http.MaxBytesReader stops the read at the boundary rather than after the
// allocation, so an oversized body is never held in memory.
func LimitRequestBody(maxBytes int64, logger *zerolog.Logger, next http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRequestBytes
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Content-Length is advisory, so it is used only as an early exit for
		// an honest client. MaxBytesReader below is what actually enforces the
		// bound for a client that lies or omits the header.
		if r.ContentLength > maxBytes {
			reject(w, maxBytes)
			logger.Warn().
				Int64("content_length", r.ContentLength).
				Int64("max_bytes", maxBytes).
				Str("path", r.URL.Path).
				Msg("Rejected oversized request by declared length")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// reject answers with a JSON-RPC shaped 413 so an MCP client surfaces the
// reason rather than a bare transport failure.
func reject(w http.ResponseWriter, maxBytes int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)

	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32600,
			"message": "Request body exceeds the configured maximum.",
			"data":    map[string]any{"maxBytes": maxBytes},
		},
	})
}
