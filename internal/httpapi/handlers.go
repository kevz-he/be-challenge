package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

const maxBodyBytes = 8 << 20 // 8 MiB

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrInvalidInput, err)
	}
	return nil
}

<<<<<<< Updated upstream
=======
// requireJSONContentType returns errUnsupportedMediaType if the request
// does not declare a JSON Content-Type. Both an empty Content-Type and any
// non-JSON media type are rejected: ingest endpoints carry a JSON body, so
// we require the client to be explicit about it (RFC 7807 §3 maps to 415).
func requireJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return fmt.Errorf("%w: missing Content-Type, expected application/json", errUnsupportedMediaType)
	}
	mt := ct
	if i := strings.Index(ct, ";"); i >= 0 {
		mt = ct[:i]
	}
	mt = strings.TrimSpace(strings.ToLower(mt))
	if mt == "application/json" || strings.HasSuffix(mt, "+json") {
		return nil
	}
	return fmt.Errorf("%w: expected application/json, got %q", errUnsupportedMediaType, ct)
}

>>>>>>> Stashed changes
func ingestSingleHandler(svc *service.IngestService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tx domain.Transaction
		if err := decodeJSON(r, &tx); err != nil {
			respondError(w, r, err)
			return
		}
		if err := svc.Single(r.Context(), tx); err != nil {
			respondError(w, r, err)
			return
		}
		respondJSON(w, http.StatusCreated, map[string]any{"accepted": 1})
	}
}

type batchRequest struct {
	Transactions []domain.Transaction `json:"transactions"`
}

func ingestBatchHandler(svc *service.IngestService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req batchRequest
		if err := decodeJSON(r, &req); err != nil {
			respondError(w, r, err)
			return
		}
		res, err := svc.Batch(r.Context(), req.Transactions)
		if err != nil {
			if errors.Is(err, domain.ErrInvalidInput) && len(res.Errors) > 0 {
				respondProblem(w, r, http.StatusBadRequest, "Bad request",
					"one or more transactions are invalid; nothing was persisted",
					map[string]any{"errors": res.Errors, "accepted": 0},
				)
				return
			}
			respondError(w, r, err)
			return
		}
		respondJSON(w, http.StatusCreated, res)
	}
}

func healthHandler(svc *service.HealthService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		win, err := parseWindow(r)
		if err != nil {
			respondError(w, r, err)
			return
		}
		bd, err := parseBreakdown(r)
		if err != nil {
			respondError(w, r, err)
			return
		}
		rep, err := svc.Compute(r.Context(), win, bd)
		if err != nil {
			respondError(w, r, err)
			return
		}
		respondJSON(w, http.StatusOK, rep)
	}
}

func anomaliesHandler(svc *service.AnomaliesService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		win, err := parseWindow(r)
		if err != nil {
			respondError(w, r, err)
			return
		}
		typeStr := r.URL.Query().Get("type")
		if typeStr == "" {
			sum, err := svc.Summary(r.Context(), win)
			if err != nil {
				respondError(w, r, err)
				return
			}
			respondJSON(w, http.StatusOK, sum)
			return
		}
		t := domain.AnomalyType(typeStr)
		if !t.Valid() {
			respondError(w, r, fmt.Errorf("%w: invalid type %q", domain.ErrInvalidInput, typeStr))
			return
		}
		limit, err := parseLimit(r, 50)
		if err != nil {
			respondError(w, r, err)
			return
		}
		offset, err := parseOffset(r)
		if err != nil {
			respondError(w, r, err)
			return
		}
		res, err := svc.List(r.Context(), service.AnomalyQuery{
			Type:   t,
			Window: win,
			Limit:  limit,
			Offset: offset,
		})
		if err != nil {
			respondError(w, r, err)
			return
		}
		respondJSON(w, http.StatusOK, res)
	}
}

func alertsHandler(svc *service.AlertsService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		win, err := parseWindow(r)
		if err != nil {
			respondError(w, r, err)
			return
		}
		rep, err := svc.Evaluate(r.Context(), win)
		if err != nil {
			respondError(w, r, err)
			return
		}
		respondJSON(w, http.StatusOK, rep)
	}
}
