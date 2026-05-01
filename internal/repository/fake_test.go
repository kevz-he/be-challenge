package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

type expectedOracle struct {
	WindowFrom           time.Time      `json:"window_from"`
	WindowTo             time.Time      `json:"window_to"`
	Now                  time.Time      `json:"now"`
	TotalRows            int            `json:"total_rows"`
	UniqueTransactionIDs int            `json:"unique_transaction_ids_in_window"`
	AnomalyCounts        map[string]int `json:"anomaly_counts"`
	DuplicateExtraRows   int            `json:"duplicate_extra_rows"`
}

func loadFixturesAndOracle(t *testing.T) ([]domain.Transaction, expectedOracle) {
	t.Helper()
	root := repoRoot(t)
	txPath := filepath.Join(root, "testdata", "transactions.json")
	expPath := filepath.Join(root, "testdata", "expected_counts.json")

	rawTx, err := os.ReadFile(txPath)
	if err != nil {
		t.Skipf("missing %s: run `make seed` first", txPath)
	}
	var txs []domain.Transaction
	if err := json.Unmarshal(rawTx, &txs); err != nil {
		t.Fatalf("unmarshal transactions: %v", err)
	}

	rawExp, err := os.ReadFile(expPath)
	if err != nil {
		t.Skipf("missing %s: run `make seed` first", expPath)
	}
	var exp expectedOracle
	if err := json.Unmarshal(rawExp, &exp); err != nil {
		t.Fatalf("unmarshal expected: %v", err)
	}
	return txs, exp
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
	t.Fatalf("go.mod not found upwards from %s", wd)
	return ""
}

func TestFake_AnomaliesAgainstOracle(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := repository.NewFake()
	ctx := context.Background()
	if err := repo.InsertBatch(ctx, txs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}

	counts, err := repo.CountsByWindow(ctx, w, exp.Now, th)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts.Orphaned != exp.AnomalyCounts["orphaned"] {
		t.Errorf("orphaned: got %d want %d", counts.Orphaned, exp.AnomalyCounts["orphaned"])
	}
	if counts.Ghost != exp.AnomalyCounts["ghost"] {
		t.Errorf("ghost: got %d want %d", counts.Ghost, exp.AnomalyCounts["ghost"])
	}
	if counts.DuplicateGroups != exp.AnomalyCounts["duplicate"] {
		t.Errorf("duplicate groups: got %d want %d", counts.DuplicateGroups, exp.AnomalyCounts["duplicate"])
	}
	if counts.DuplicateExtra != exp.DuplicateExtraRows {
		t.Errorf("duplicate extra: got %d want %d", counts.DuplicateExtra, exp.DuplicateExtraRows)
	}
	if counts.PendingLimbo != exp.AnomalyCounts["pending_limbo"] {
		t.Errorf("pending_limbo: got %d want %d", counts.PendingLimbo, exp.AnomalyCounts["pending_limbo"])
	}
	if counts.UniqueInWindow != exp.UniqueTransactionIDs {
		t.Errorf("unique: got %d want %d", counts.UniqueInWindow, exp.UniqueTransactionIDs)
	}
}

func TestWindow_Validate(t *testing.T) {
	from := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 6, 0, 0, 0, time.UTC)

	if err := (repository.Window{From: &from, To: &to}).Validate(); err != nil {
		t.Errorf("valid window failed: %v", err)
	}
	if err := (repository.Window{From: &to, To: &from}).Validate(); !errors.Is(err, domain.ErrInvalidWindow) {
		t.Errorf("expected ErrInvalidWindow, got %v", err)
	}
	if err := (repository.Window{}).Validate(); err != nil {
		t.Errorf("empty window failed: %v", err)
	}
}

func TestAnomalyFilter_Validate(t *testing.T) {
	if err := (repository.AnomalyFilter{Limit: -1}).Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for limit, got %v", err)
	}
	if err := (repository.AnomalyFilter{Offset: -1}).Validate(); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for offset, got %v", err)
	}
}

func TestFake_EmptyDataset(t *testing.T) {
	repo := repository.NewFake()
	ctx := context.Background()
	uniq, err := repo.UniqueTransactionIDsCount(ctx, repository.Window{})
	if err != nil {
		t.Fatal(err)
	}
	if uniq != 0 {
		t.Errorf("expected 0, got %d", uniq)
	}
	c, err := repo.CountsByWindow(ctx, repository.Window{}, time.Now(), repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if c.Orphaned != 0 || c.Ghost != 0 || c.DuplicateGroups != 0 || c.PendingLimbo != 0 {
		t.Errorf("expected zero counts, got %+v", c)
	}
}
