package api

import (
	"context"
	"net/http"
	"time"

	"github.com/deigmata-paideias/gateway/internal/gateway"
)

type operationsAPI struct {
	service      *gateway.Service
	readyTimeout time.Duration
}

func NewOperationsHandler(service *gateway.Service, readyTimeout time.Duration) http.Handler {
	api := &operationsAPI{service: service, readyTimeout: readyTimeout}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", api.live)
	mux.HandleFunc("GET /readyz", api.ready)
	mux.HandleFunc("GET /healthz", api.ready)
	return requestIDMiddleware(mux)
}

func (a *operationsAPI) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
}

func (a *operationsAPI) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), a.readyTimeout)
	defer cancel()
	if a.service.CurrentSnapshot() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "config"})
		return
	}
	if err := a.service.Store().Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "storage"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
