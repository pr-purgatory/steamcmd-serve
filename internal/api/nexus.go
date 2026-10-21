package api

import (
	"context"
	"errors"
	"net/http"

	"steamcmd-serve/internal/mods/nexus"
)

// maxNexusKey bounds the accepted API key length (real keys are ~100 characters).
const maxNexusKey = 200

// NexusSettings stores the NexusMods API key. *nexus.Keyring implements it.
type NexusSettings interface {
	Set(ctx context.Context, key string) (nexus.Account, error)
	Status(ctx context.Context) (nexus.Account, error)
	Delete(ctx context.Context) error
}

var _ NexusSettings = (*nexus.Keyring)(nil)

// WithNexus enables the /settings/nexus-api-key routes. With nil they answer 501.
func (h *Handler) WithNexus(n NexusSettings) *Handler {
	h.nexus = n
	return h
}

// The API key is never logged, echoed or included in an error message.
func (h *Handler) setNexusKey(w http.ResponseWriter, r *http.Request) {
	if h.nexus == nil {
		notImplemented(w, r)
		return
	}
	var body struct {
		APIKey string `json:"apiKey"`
	}
	if !decodeBody(w, r, &body, true) {
		return
	}
	if body.APIKey == "" || len(body.APIKey) > maxNexusKey {
		badRequest(w, "apiKey must be 1 to 200 characters")
		return
	}
	acct, err := h.nexus.Set(r.Context(), body.APIKey)
	switch {
	case errors.Is(err, nexus.ErrInvalidKey):
		writeError(w, http.StatusBadRequest, "invalid", "NexusMods rejected this API key")
	case errors.Is(err, nexus.ErrRateLimited):
		writeError(w, http.StatusBadRequest, "invalid", "NexusMods rate limit reached, try again later")
	case err != nil:
		writeErr(w, r, err)
	default:
		writeJSON(w, http.StatusOK, acct)
	}
}

func (h *Handler) getNexusKey(w http.ResponseWriter, r *http.Request) {
	if h.nexus == nil {
		notImplemented(w, r)
		return
	}
	acct, err := h.nexus.Status(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

func (h *Handler) deleteNexusKey(w http.ResponseWriter, r *http.Request) {
	if h.nexus == nil {
		notImplemented(w, r)
		return
	}
	if err := h.nexus.Delete(r.Context()); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
