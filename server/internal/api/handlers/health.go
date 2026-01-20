package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type HealthHandler struct {
	repo *storage.Repository
}

func NewHealthHandler(repo *storage.Repository) *HealthHandler {
	return &HealthHandler{repo: repo}
}

type HealthResponse struct {
	Status    string `json:"status"`
	Database  string `json:"database"`
	Version   string `json:"version"`
}

func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if h.repo == nil {
		dbStatus = "not configured"
	} else if err := h.repo.Ping(r.Context()); err != nil {
		dbStatus = "error: " + err.Error()
	}

	status := "ok"
	if dbStatus != "ok" {
		status = "degraded"
	}

	resp := HealthResponse{
		Status:   status,
		Database: dbStatus,
		Version:  "1.0.0",
	}

	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(resp)
}
