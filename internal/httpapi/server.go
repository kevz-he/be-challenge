// Package httpapi implements the HTTP router, handlers and shared helpers.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

// Deps groups what a Server needs to answer requests.
type Deps struct {
	Logger         *slog.Logger
	Ingest         *service.IngestService
	Health         *service.HealthService
	Anomalies      *service.AnomaliesService
	Alerts         *service.AlertsService // may be nil if stretches are disabled
	SQLiteRepo     *sqlite.Repo           // only for the DB healthcheck; may be nil
	RequestTimeout time.Duration
}

// NewRouter builds the chi router with middleware and routes.
func NewRouter(d Deps) *chi.Mux {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.RequestTimeout <= 0 {
		d.RequestTimeout = 30 * time.Second
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(slogRequestLogger(d.Logger))
	r.Use(middleware.Timeout(d.RequestTimeout))

	r.Get("/healthz", livenessHandler(d.SQLiteRepo))

	r.Route("/v1", func(r chi.Router) {
		r.Post("/transactions", ingestSingleHandler(d.Ingest))
		r.Post("/transactions/batch", ingestBatchHandler(d.Ingest))
		r.Get("/health", healthHandler(d.Health))
		r.Get("/anomalies", anomaliesHandler(d.Anomalies))
		if d.Alerts != nil {
			r.Get("/alerts", alertsHandler(d.Alerts))
		}
	})
	return r
}

func slogRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
					slog.String("request_id", middleware.GetReqID(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("query", r.URL.RawQuery),
					slog.String("remote", r.RemoteAddr),
					slog.Int("status", ww.Status()),
					slog.Int("bytes", ww.BytesWritten()),
					slog.Duration("duration", time.Since(start)),
				)
			}()
			next.ServeHTTP(ww, r)
		})
	}
}

func livenessHandler(repo *sqlite.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if repo != nil {
			if err := repo.DB().PingContext(r.Context()); err != nil {
				respondJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down", "error": err.Error()})
				return
			}
		}
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
