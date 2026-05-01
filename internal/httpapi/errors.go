package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/yuno/transaction-health-monitor/internal/domain"
)

// Problem is the application/problem+json response body (RFC 7807).
type Problem struct {
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Status   int            `json:"status"`
	Detail   string         `json:"detail,omitempty"`
	Instance string         `json:"instance,omitempty"`
	Errors   any            `json:"errors,omitempty"`
	Extras   map[string]any `json:"-"`
}

const problemContentType = "application/problem+json"

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		slog.Default().Error("encode response", "err", err)
	}
}

func respondProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string, extras map[string]any) {
	w.Header().Set("Content-Type", problemContentType)
	p := Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: r.URL.Path,
	}
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	merged := map[string]any{
		"type":     p.Type,
		"title":    p.Title,
		"status":   p.Status,
		"detail":   p.Detail,
		"instance": p.Instance,
	}
	for k, v := range extras {
		merged[k] = v
	}
	if err := enc.Encode(merged); err != nil {
		slog.Default().Error("encode problem", "err", err)
	}
}

func respondError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidWindow):
		respondProblem(w, r, http.StatusUnprocessableEntity, "Invalid time window", err.Error(), nil)
	case errors.Is(err, domain.ErrInvalidInput):
		respondProblem(w, r, http.StatusBadRequest, "Bad request", err.Error(), nil)
	case errors.Is(err, domain.ErrNotFound):
		respondProblem(w, r, http.StatusNotFound, "Not found", err.Error(), nil)
	default:
		slog.Default().Error("internal error", "err", err, "path", r.URL.Path)
		respondProblem(w, r, http.StatusInternalServerError, "Internal server error", "an unexpected error occurred", nil)
	}
}
