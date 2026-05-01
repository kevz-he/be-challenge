package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/httpapi"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

func newTestServer(t *testing.T) (*httptest.Server, *repository.Fake, *clock.Fake) {
	t.Helper()
	repo := repository.NewFake()
	clk := clock.NewFake(time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC))
	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	ingest := service.NewIngestService(repo)
	health := service.NewHealthService(repo, clk, th)
	anomalies := service.NewAnomaliesService(repo, clk, th)
	alerts := service.NewAlertsService(repo, clk, th, service.AlertThresholds{
		OrphanedMax: 50, GhostMax: 100, HealthMin: 0.95,
	}, health)

	router := httpapi.NewRouter(httpapi.Deps{
		Ingest:    ingest,
		Health:    health,
		Anomalies: anomalies,
		Alerts:    alerts,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, repo, clk
}

func validBody() string {
	return `{
		"transaction_id": "abc",
		"occurred_at":    "2026-04-15T01:00:00Z",
		"amount_cents":   1234,
		"currency":       "BRL",
		"payment_method": "credit_card",
		"processor":      "stripe_br",
		"status":         "approved",
		"source":         "processor"
	}`
}

func TestPostTransaction_Created(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v1/transactions", "application/json", strings.NewReader(validBody()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestPostTransaction_InvalidJSON(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Post(srv.URL+"/v1/transactions", "application/json", strings.NewReader("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("expected problem+json, got %s", ct)
	}
}

func TestPostTransaction_InvalidField(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := strings.Replace(validBody(), `"BRL"`, `"USD"`, 1)
	resp, err := http.Post(srv.URL+"/v1/transactions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for currency != BRL, got %d", resp.StatusCode)
	}
}

func TestPostBatch_AtomicReject(t *testing.T) {
	srv, repo, _ := newTestServer(t)
	body := `{"transactions":[
		` + validBody() + `,
		{"transaction_id":"","occurred_at":"2026-04-15T01:00:00Z","amount_cents":1,"currency":"BRL","payment_method":"pix","processor":"x","status":"approved","source":"processor"}
	]}`
	resp, err := http.Post(srv.URL+"/v1/transactions/batch", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	listed, _ := repo.ListByWindow(context.Background(), repository.Window{}, 0, 0)
	if len(listed) != 0 {
		t.Errorf("nothing should have persisted, got %d", len(listed))
	}
}

func TestGetHealth_Empty(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rep service.HealthReport
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Score != 1.0 {
		t.Errorf("empty dataset should score 1.0, got %v", rep.Score)
	}
}

func TestGetHealth_InvalidWindow(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/health?from=2026-04-15T10:00:00Z&to=2026-04-15T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", resp.StatusCode)
	}
}

func TestGetAnomalies_BadType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/anomalies?type=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetAnomalies_Summary(t *testing.T) {
	srv, repo, _ := newTestServer(t)
	tx := domain.Transaction{}
	if err := json.NewDecoder(strings.NewReader(validBody())).Decode(&tx); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertBatch(context.Background(), []domain.Transaction{tx}); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(srv.URL + "/v1/anomalies?from=2026-04-15T00:00:00Z&to=2026-04-15T06:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var sum service.AnomalySummary
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.Counts["orphaned"] != 1 {
		t.Errorf("expected 1 orphaned, got %d", sum.Counts["orphaned"])
	}
}

func TestHealthz(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPostBatch_Success(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := `{"transactions":[` + validBody() + `]}`
	resp, err := http.Post(srv.URL+"/v1/transactions/batch", "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var res service.IngestResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Errorf("accepted: got %d want 1", res.Accepted)
	}
}

func TestGetAlerts_NoTrigger(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/alerts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var rep service.AlertReport
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Alert {
		t.Errorf("expected no alert with empty dataset, got %+v", rep)
	}
}
