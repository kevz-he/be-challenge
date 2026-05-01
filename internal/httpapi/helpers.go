package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// parseWindow reads from / to from the query string as RFC3339. If either
// is empty it stays nil. Validates from <= to via Window.Validate().
func parseWindow(r *http.Request) (repository.Window, error) {
	q := r.URL.Query()
	var w repository.Window
	if s := q.Get("from"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return w, fmt.Errorf("%w: from must be RFC3339 (%v)", domain.ErrInvalidInput, err)
		}
		t = t.UTC()
		w.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return w, fmt.Errorf("%w: to must be RFC3339 (%v)", domain.ErrInvalidInput, err)
		}
		t = t.UTC()
		w.To = &t
	}
	if err := w.Validate(); err != nil {
		return w, err
	}
	return w, nil
}

func parseLimit(r *http.Request, def int) (int, error) {
	s := r.URL.Query().Get("limit")
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%w: limit must be int", domain.ErrInvalidInput)
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: limit < 0", domain.ErrInvalidInput)
	}
	return n, nil
}

func parseOffset(r *http.Request) (int, error) {
	s := r.URL.Query().Get("offset")
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%w: offset must be int", domain.ErrInvalidInput)
	}
	if n < 0 {
		return 0, fmt.Errorf("%w: offset < 0", domain.ErrInvalidInput)
	}
	return n, nil
}

func parseBreakdown(r *http.Request) (string, error) {
	s := r.URL.Query().Get("breakdown")
	switch s {
	case "", "processor", "payment_method":
		return s, nil
	default:
		return "", fmt.Errorf("%w: breakdown must be processor|payment_method", domain.ErrInvalidInput)
	}
}
