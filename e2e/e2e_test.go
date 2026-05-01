//go:build e2e

// Package e2e wires the application exactly like cmd/server/main.go and
// drives it through HTTP. It proves the milestone end-to-end:
//
//	seed → POST /v1/transactions/batch → query /v1/health and /v1/anomalies
//	→ assert counts + ID sets + health score against testdata/expected_counts.json
//
// Golden response-shape comparisons live in
// internal/httpapi/golden_test.go (TestHTTP_GoldenResponses) so they can
// run without the e2e build tag and reuse the same fixtures.
//
// Run with:
//
//	go test ./e2e -tags=e2e -race -count=1
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/httpapi"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

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

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for cur := wd; cur != "/" && cur != ""; cur = filepath.Dir(cur) {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
	}
	t.Fatal("go.mod not found")
	return ""
}

func loadFixtures(t *testing.T) ([]domain.Transaction, expectedOracle) {
	t.Helper()
	root := repoRoot(t)
	rawTx, err := os.ReadFile(filepath.Join(root, "testdata", "transactions.json"))
	if err != nil {
		t.Skipf("missing fixtures (run make seed): %v", err)
	}
	var txs []domain.Transaction
	if err := json.Unmarshal(rawTx, &txs); err != nil {
		t.Fatalf("unmarshal txs: %v", err)
	}
	rawExp, err := os.ReadFile(filepath.Join(root, "testdata", "expected_counts.json"))
	if err != nil {
		t.Skipf("missing oracle: %v", err)
	}
	var exp expectedOracle
	if err := json.Unmarshal(rawExp, &exp); err != nil {
		t.Fatalf("unmarshal oracle: %v", err)
	}
	return txs, exp
}

// TestE2E_FullFlow_SeedIngestQueryCounts proves the milestone: it wires the
// app exactly like cmd/server, ingests the seed dataset, then queries every
// anomaly endpoint and asserts:
//   - counts match the oracle
//   - the EXACT set of transaction_id matches the oracle (set equality)
//   - the health_score matches the oracle
//
// Body shape (golden) comparisons live in
// internal/httpapi/golden_test.go.
func TestE2E_FullFlow_SeedIngestQueryCounts(t *testing.T) {
	txs, exp := loadFixtures(t)

	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer repo.Close()
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	clk := clock.NewFake(exp.Now)
	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	ingest := service.NewIngestService(repo)
	health := service.NewHealthService(repo, clk, th)
	anomalies := service.NewAnomaliesService(repo, clk, th)
	alerts := service.NewAlertsService(repo, clk, th, service.AlertThresholds{
		OrphanedMax: 50, GhostMax: 100, HealthMin: 0.95,
	}, health)
	router := httpapi.NewRouter(httpapi.Deps{
		Ingest: ingest, Health: health, Anomalies: anomalies, Alerts: alerts,
	})
	srv := httptest.NewServer(router)
	defer srv.Close()

	body, err := json.Marshal(map[string]any{"transactions": txs})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/v1/transactions/batch", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("ingest http %d: %s", resp.StatusCode, b)
	}

	q := "?from=" + exp.WindowFrom.Format("2006-01-02T15:04:05Z") +
		"&to=" + exp.WindowTo.Format("2006-01-02T15:04:05Z")

	var hr struct {
		Score              float64        `json:"score"`
		AnomalyCounts      map[string]int `json:"anomaly_counts"`
		DuplicateExtraRows int            `json:"duplicate_extra_rows"`
	}
	getJSON(t, srv.URL+"/v1/health"+q, &hr)
	wantScore := exp.ExpectedHealthScore
	if wantScore == 0 {
		wantScore = exp.HealthScore
	}
	if math.Abs(hr.Score-wantScore) > 1e-9 {
		t.Errorf("health score: got %.12f want %.12f", hr.Score, wantScore)
	}
	for _, k := range []string{"orphaned", "ghost", "duplicate", "pending_limbo"} {
		if hr.AnomalyCounts[k] != exp.AnomalyCounts[k] {
			t.Errorf("count %s: got %d want %d", k, hr.AnomalyCounts[k], exp.AnomalyCounts[k])
		}
	}
	if hr.DuplicateExtraRows != exp.DuplicateExtraRows {
		t.Errorf("duplicate_extra_rows: got %d want %d", hr.DuplicateExtraRows, exp.DuplicateExtraRows)
	}

	for _, kind := range []string{"orphaned", "ghost", "pending_limbo"} {
		var page struct {
			Items []struct {
				TransactionID string `json:"transaction_id"`
			} `json:"items"`
		}
		getJSON(t, srv.URL+"/v1/anomalies"+q+"&type="+kind+"&limit=1000", &page)
		got := make([]string, 0, len(page.Items))
		for _, it := range page.Items {
			got = append(got, it.TransactionID)
		}
		if !sameSet(got, exp.IDs[kind]) {
			t.Errorf("%s ID set mismatch.\n got=%v\n want=%v", kind, sortedCopy(got), sortedCopy(exp.IDs[kind]))
		}
	}

	var dupPage struct {
		Groups []struct {
			TransactionID string `json:"transaction_id"`
		} `json:"groups"`
	}
	getJSON(t, srv.URL+"/v1/anomalies"+q+"&type=duplicate&limit=1000", &dupPage)
	got := make([]string, 0, len(dupPage.Groups))
	seen := map[string]struct{}{}
	for _, g := range dupPage.Groups {
		if _, ok := seen[g.TransactionID]; ok {
			continue
		}
		seen[g.TransactionID] = struct{}{}
		got = append(got, g.TransactionID)
	}
	if !sameSet(got, exp.IDs["duplicate"]) {
		t.Errorf("duplicate ID set mismatch.\n got=%v\n want=%v", sortedCopy(got), sortedCopy(exp.IDs["duplicate"]))
	}
}

func getJSON(t *testing.T, url string, dst any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s -> %d: %s", url, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[string]struct{}{}
	for _, s := range a {
		ma[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := ma[s]; !ok {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
