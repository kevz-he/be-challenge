package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

type expectedOracle struct {
	WindowFrom           time.Time      `json:"window_from"`
	WindowTo             time.Time      `json:"window_to"`
	Now                  time.Time      `json:"now"`
	UniqueTransactionIDs int            `json:"unique_transaction_ids_in_window"`
	AnomalyCounts        map[string]int `json:"anomaly_counts"`
	DuplicateExtraRows   int            `json:"duplicate_extra_rows"`
	HealthScore          float64        `json:"health_score"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	for cur := wd; cur != "/" && cur != ""; cur = filepath.Dir(cur) {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
	}
	t.Fatal("go.mod not found")
	return ""
}

func loadOracleFixtures(t *testing.T) ([]domain.Transaction, expectedOracle) {
	t.Helper()
	root := repoRoot(t)
	rawTx, err := os.ReadFile(filepath.Join(root, "testdata", "transactions.json"))
	if err != nil {
		t.Skipf("missing testdata: run `make seed` first (%v)", err)
	}
	var txs []domain.Transaction
	if err := json.Unmarshal(rawTx, &txs); err != nil {
		t.Fatal(err)
	}
	rawExp, err := os.ReadFile(filepath.Join(root, "testdata", "expected_counts.json"))
	if err != nil {
		t.Skipf("missing expected_counts: run `make seed` first (%v)", err)
	}
	var exp expectedOracle
	if err := json.Unmarshal(rawExp, &exp); err != nil {
		t.Fatal(err)
	}
	return txs, exp
}

func makeFakeWith(t *testing.T) (*repository.Fake, expectedOracle) {
	t.Helper()
	txs, exp := loadOracleFixtures(t)
	repo := repository.NewFake()
	if err := repo.InsertBatch(context.Background(), txs); err != nil {
		t.Fatal(err)
	}
	return repo, exp
}

func validTx() domain.Transaction {
	return domain.Transaction{
		TransactionID: "abc-123",
		OccurredAt:    time.Now().UTC(),
		AmountCents:   1000,
		Currency:      domain.CurrencyBRL,
		PaymentMethod: domain.PaymentMethodCreditCard,
		Processor:     "stripe_br",
		Status:        domain.StatusApproved,
		Source:        domain.SourceProcessor,
	}
}

func TestIngest_Single_Valid(t *testing.T) {
	repo := repository.NewFake()
	svc := service.NewIngestService(repo)
	if err := svc.Single(context.Background(), validTx()); err != nil {
		t.Fatal(err)
	}
}

func TestIngest_Single_Invalid(t *testing.T) {
	repo := repository.NewFake()
	svc := service.NewIngestService(repo)
	tx := validTx()
	tx.AmountCents = -1
	err := svc.Single(context.Background(), tx)
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestIngest_BatchAtomic(t *testing.T) {
	repo := repository.NewFake()
	svc := service.NewIngestService(repo)
	good := validTx()
	bad := validTx()
	bad.TransactionID = ""

	res, err := svc.Batch(context.Background(), []domain.Transaction{good, bad})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if res.Accepted != 0 {
		t.Errorf("accepted should be 0 in atomic batch, got %d", res.Accepted)
	}
	if len(res.Errors) != 1 || res.Errors[0].Index != 1 {
		t.Errorf("expected single error at index 1, got %+v", res.Errors)
	}
	listed, _ := repo.ListByWindow(context.Background(), repository.Window{}, 0, 0)
	if len(listed) != 0 {
		t.Errorf("nothing should have persisted, got %d rows", len(listed))
	}
}

func TestIngest_BatchAllValid(t *testing.T) {
	repo := repository.NewFake()
	svc := service.NewIngestService(repo)
	a, b := validTx(), validTx()
	b.TransactionID = "xyz-456"
	res, err := svc.Batch(context.Background(), []domain.Transaction{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 {
		t.Errorf("accepted: got %d want 2", res.Accepted)
	}
}

func TestHealth_AgainstOracle(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewHealthService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	rep, err := svc.Compute(context.Background(), w, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.UniqueTransactions != exp.UniqueTransactionIDs {
		t.Errorf("unique: got %d want %d", rep.UniqueTransactions, exp.UniqueTransactionIDs)
	}
	if rep.AnomalyCounts["orphaned"] != exp.AnomalyCounts["orphaned"] {
		t.Errorf("orphaned: got %d want %d", rep.AnomalyCounts["orphaned"], exp.AnomalyCounts["orphaned"])
	}
	if rep.DuplicateExtraRows != exp.DuplicateExtraRows {
		t.Errorf("dup extra: got %d want %d", rep.DuplicateExtraRows, exp.DuplicateExtraRows)
	}
	delta := rep.Score - exp.HealthScore
	if delta < -1e-9 || delta > 1e-9 {
		t.Errorf("score: got %v want %v", rep.Score, exp.HealthScore)
	}
}

func TestHealth_EmptyDatasetReturns1(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	svc := service.NewHealthService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	rep, err := svc.Compute(context.Background(), repository.Window{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Score != 1.0 {
		t.Errorf("expected score=1.0 for empty dataset, got %v", rep.Score)
	}
}

func TestHealth_BreakdownByProcessor(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewHealthService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	rep, err := svc.Compute(context.Background(), w, "processor")
	if err != nil {
		t.Fatal(err)
	}
	if rep.BreakdownBy != "processor" {
		t.Errorf("breakdown_by: got %q", rep.BreakdownBy)
	}
	if len(rep.Breakdown) == 0 {
		t.Errorf("breakdown empty")
	}
}

func TestHealth_InvalidWindow(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	svc := service.NewHealthService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	from := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	_, err := svc.Compute(context.Background(), repository.Window{From: &from, To: &to}, "")
	if !errors.Is(err, domain.ErrInvalidWindow) {
		t.Errorf("expected ErrInvalidWindow, got %v", err)
	}
}

func TestAnomalies_List(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	res, err := svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyOrphaned, Window: w, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 5 {
		t.Errorf("with limit=5 expected 5 items, got %d", len(res.Items))
	}
}

func TestAnomalies_ListInvalidType(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	_, err := svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyType("nope")})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAnomalies_Summary(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	sum, err := svc.Summary(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Counts["orphaned"] != exp.AnomalyCounts["orphaned"] {
		t.Errorf("summary orphaned mismatch")
	}
	for _, kind := range []string{"orphaned", "ghost", "duplicate", "pending_limbo"} {
		s := sum.Samples[kind]
		if s == nil {
			t.Errorf("nil sample for %s", kind)
		}
		if len(s) > 5 {
			t.Errorf("sample for %s exceeded 5 (got %d)", kind, len(s))
		}
	}
}
