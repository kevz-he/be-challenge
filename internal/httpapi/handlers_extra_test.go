package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/httpapi"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

// newTestServerSQLite wires the real SQLite repo (in-memory) instead of the
// fake; necessary to load the seed dataset and exercise paths that involve
// sqlite-specific behavior (window edges, breakdowns, etc).
func newTestServerSQLite(t *testing.T, now time.Time) (*httptest.Server, *sqlite.Repo) {
	t.Helper()
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	clk := clock.NewFake(now)
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
	return srv, repo
}

// 415 -------------------------------------------------------------------

func TestPostTransaction_UnsupportedMediaType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/transactions", strings.NewReader(validBody()))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("expected problem+json, got %s", ct)
	}
	var p map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "title", "status", "detail"} {
		if _, ok := p[k]; !ok {
			t.Errorf("problem+json missing %q field", k)
		}
	}
}

func TestPostTransaction_MissingContentType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/transactions", strings.NewReader(validBody()))
	// Go's stdlib client adds a Content-Type for us if we don't strip it.
	req.Header.Del("Content-Type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415 for missing Content-Type, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "problem+json") {
		t.Errorf("expected problem+json, got %s", ct)
	}
}

func TestPostBatch_UnsupportedMediaType(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/transactions/batch", strings.NewReader(`{"transactions":[]}`))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("expected 415, got %d", resp.StatusCode)
	}
}

// problem+json shape ----------------------------------------------------

func TestProblemJSON_Shape_BadRequest(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/anomalies?type=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("expected application/problem+json, got %s", ct)
	}
	var p map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "title", "status", "detail", "instance"} {
		if _, ok := p[k]; !ok {
			t.Errorf("problem+json missing %q", k)
		}
	}
	if status, _ := p["status"].(float64); int(status) != http.StatusBadRequest {
		t.Errorf("status field mismatch: %v", p["status"])
	}
}

// breakdown -------------------------------------------------------------

func TestGetAnomalies_BreakdownByProcessor(t *testing.T) {
	srv, repo := newTestServerSQLite(t, time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC))
	now := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insertSlice(t, repo,
		txOf("X1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, "stripe_br", now),
		txOf("X2", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, "cielo", now),
		txOf("X3", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, "stripe_br", now),
	)

	resp, err := http.Get(srv.URL + "/v1/anomalies?breakdown=processor")
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
	if sum.BreakdownBy != "processor" {
		t.Errorf("breakdown_by: %s", sum.BreakdownBy)
	}
	if sum.Breakdown["stripe_br"]["orphaned"] != 2 {
		t.Errorf("expected 2 orphaned for stripe_br, got %+v", sum.Breakdown["stripe_br"])
	}
	if sum.Breakdown["cielo"]["orphaned"] != 1 {
		t.Errorf("expected 1 orphaned for cielo, got %+v", sum.Breakdown["cielo"])
	}
}

func TestGetAnomalies_BreakdownInvalid(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/anomalies?breakdown=banana")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// paging ----------------------------------------------------------------

func TestGetAnomalies_Paging(t *testing.T) {
	srv, repo := newTestServerSQLite(t, time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC))
	at := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		insertSlice(t, repo, txOf(makeID(i), domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, "stripe_br", at.Add(time.Duration(i)*time.Second)))
	}
	resp, err := http.Get(srv.URL + "/v1/anomalies?type=orphaned&limit=4&offset=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var page service.AnomalyListResult
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 4 {
		t.Errorf("expected 4 items, got %d", len(page.Items))
	}
	if page.Paging.Limit != 4 || page.Paging.Offset != 0 {
		t.Errorf("paging: %+v", page.Paging)
	}
}

func TestGetAnomalies_NegativeLimit(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/v1/anomalies?type=orphaned&limit=-1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// Health on full dataset -----------------------------------------------

func TestHTTP_HealthMatchesExpectedScoreForFullDataset(t *testing.T) {
	exp := loadExpected(t)
	srv, repo := newTestServerSQLite(t, exp.Now)
	loadFixturesInto(t, repo)

	q := "/v1/health?from=" + exp.WindowFrom.Format(time.RFC3339) + "&to=" + exp.WindowTo.Format(time.RFC3339)
	resp, err := http.Get(srv.URL + q)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, body)
	}
	var rep service.HealthReport
	if err := json.Unmarshal(body, &rep); err != nil {
		t.Fatal(err)
	}
	want := exp.ExpectedHealthScore
	if want == 0 {
		want = exp.HealthScore
	}
	if absf(rep.Score-want) > 1e-9 {
		t.Errorf("score: got %.12f want %.12f", rep.Score, want)
	}
	if rep.AnomalyCounts["orphaned"] != exp.AnomalyCounts["orphaned"] {
		t.Errorf("orphaned: got %d want %d", rep.AnomalyCounts["orphaned"], exp.AnomalyCounts["orphaned"])
	}
}

// helpers ---------------------------------------------------------------

func txOf(id string, src domain.Source, st domain.Status, pm domain.PaymentMethod, processor string, at time.Time) domain.Transaction {
	return domain.Transaction{
		TransactionID: id,
		OccurredAt:    at.UTC(),
		AmountCents:   1234,
		Currency:      domain.CurrencyBRL,
		PaymentMethod: pm,
		Processor:     processor,
		Status:        st,
		Source:        src,
	}
}

func insertSlice(t *testing.T, repo *sqlite.Repo, txs ...domain.Transaction) {
	t.Helper()
	if err := repo.InsertBatch(context.Background(), txs); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func makeID(i int) string {
	const ids = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return string(ids[i%len(ids)]) + "X"
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// expectedOracle copies the fields of testdata/expected_counts.json that
// HTTP tests need (avoid coupling to seed.ExpectedCounts).
type expectedOracle struct {
	WindowFrom           time.Time           `json:"window_from"`
	WindowTo             time.Time           `json:"window_to"`
	Now                  time.Time           `json:"now"`
	UniqueTransactionIDs int                 `json:"unique_transaction_ids_in_window"`
	AnomalyCounts        map[string]int      `json:"anomaly_counts"`
	DuplicateExtraRows   int                 `json:"duplicate_extra_rows"`
	HealthScore          float64             `json:"health_score"`
	ExpectedHealthScore  float64             `json:"expected_health_score"`
	IDs                  map[string][]string `json:"ids"`
}

func loadExpected(t *testing.T) expectedOracle {
	t.Helper()
	data, err := readRepoFile(t, "testdata/expected_counts.json")
	if err != nil {
		t.Skipf("missing expected_counts (run make seed): %v", err)
	}
	var exp expectedOracle
	if err := json.Unmarshal(data, &exp); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	return exp
}

func loadFixturesInto(t *testing.T, repo *sqlite.Repo) {
	t.Helper()
	data, err := readRepoFile(t, "testdata/transactions.json")
	if err != nil {
		t.Skipf("missing fixtures (run make seed): %v", err)
	}
	var txs []domain.Transaction
	if err := json.Unmarshal(data, &txs); err != nil {
		t.Fatalf("unmarshal fixtures: %v", err)
	}
	if err := repo.InsertBatch(context.Background(), txs); err != nil {
		t.Fatalf("insert fixtures: %v", err)
	}
}

func readRepoFile(t *testing.T, rel string) ([]byte, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for cur := wd; cur != "/" && cur != ""; cur = filepath.Dir(cur) {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return os.ReadFile(filepath.Join(cur, rel))
		}
	}
	return nil, os.ErrNotExist
}
