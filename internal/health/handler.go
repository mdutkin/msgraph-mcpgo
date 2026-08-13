package health

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/rs/zerolog"
)

// Handler handles health check endpoints
type Handler struct {
	logger *zerolog.Logger
	ready  atomic.Bool
}

// NewHandler creates a new health check handler
func NewHandler(logger *zerolog.Logger) *Handler {
	h := &Handler{
		logger: logger,
	}
	// Initially ready
	h.ready.Store(true)
	return h
}

// SetReady sets the readiness state
func (h *Handler) SetReady(ready bool) {
	h.ready.Store(ready)
}

// LivenessHandler handles liveness probe requests
// Returns 200 if the service is running
func (h *Handler) LivenessHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]string{
		"status": "alive",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error().Err(err).Msg("Failed to encode liveness response")
	}
}

// ReadinessHandler handles readiness probe requests
// Returns 200 if the service is ready to handle requests
func (h *Handler) ReadinessHandler(w http.ResponseWriter, r *http.Request) {
	if !h.ready.Load() {
		response := map[string]string{
			"status": "not ready",
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)

		if err := json.NewEncoder(w).Encode(response); err != nil {
			h.logger.Error().Err(err).Msg("Failed to encode readiness response")
		}
		return
	}

	response := map[string]string{
		"status": "ready",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Error().Err(err).Msg("Failed to encode readiness response")
	}
}
