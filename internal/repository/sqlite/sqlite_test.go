package sqlite_test

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
	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
)

type expectedOracle struct {
	WindowFrom           time.Time      `json:"window_from"`
	WindowTo             time.Time      `json:"window_to"`
	Now                  time.Time      `json:"now"`
	TotalRows            int            `json:"total_rows"`
	UniqueTransactionIDs int            `json:"unique_transaction_ids_in_window"`
	AnomalyCounts        map[string]int `json:"anomaly_counts"`
	DuplicateExtraRows   int            `json:"duplicate_extra_rows"`
	HealthScore          float64        `json:"health_score"`
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
	t.Fatalf("go.mod not found")
	return ""
}

func loadFixturesAndOracle(t *testing.T) ([]domain.Transaction, expectedOracle) {
	t.Helper()
	root := repoRoot(t)
	rawTx, err := os.ReadFile(filepath.Join(root, "testdata", "transactions.json"))
	if err != nil {
		t.Skipf("missing testdata: run `make seed` first (%v)", err)
	}
	var txs []domain.Transaction
	if err := json.Unmarshal(rawTx, &txs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rawExp, err := os.ReadFile(filepath.Join(root, "testdata", "expected_counts.json"))
	if err != nil {
		t.Skipf("missing expected_counts: run `make seed` first (%v)", err)
	}
	var exp expectedOracle
	if err := json.Unmarshal(rawExp, &exp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return txs, exp
}

func openMem(t *testing.T) *sqlite.Repo {
	t.Helper()
	repo, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return repo
}

func TestMigrate_Idempotent(t *testing.T) {
	repo := openMem(t)
	for i := 0; i < 3; i++ {
		if err := repo.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate iter %d: %v", i, err)
		}
	}
}

func TestSQLite_AnomaliesAgainstOracle(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
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
	if counts.TotalRowsWindow != exp.TotalRows {
		t.Errorf("total rows: got %d want %d", counts.TotalRowsWindow, exp.TotalRows)
	}
}

func TestSQLite_FindOrphanedDetails(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	_ = repo.InsertBatch(ctx, txs)

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	res, err := repo.FindOrphanedApprovals(ctx, repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != exp.AnomalyCounts["orphaned"] {
		t.Fatalf("len: got %d want %d", len(res), exp.AnomalyCounts["orphaned"])
	}
	for _, d := range res {
		if d.Source != domain.SourceProcessor {
			t.Errorf("orphan must be processor source")
		}
		if d.Status != domain.StatusApproved {
			t.Errorf("orphan must be approved")
		}
		if d.Reason == "" {
			t.Errorf("orphan reason empty")
		}
	}
}

func TestSQLite_DuplicatesShape(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	_ = repo.InsertBatch(ctx, txs)

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	groups, err := repo.FindDuplicates(ctx, repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != exp.AnomalyCounts["duplicate"] {
		t.Fatalf("groups len: got %d want %d", len(groups), exp.AnomalyCounts["duplicate"])
	}
	for _, g := range groups {
		if g.Occurrences < 2 {
			t.Errorf("group with <2 rows: %+v", g)
		}
		if len(g.Rows) != g.Occurrences {
			t.Errorf("rows %d != occurrences %d", len(g.Rows), g.Occurrences)
		}
	}
}

func TestSQLite_PendingLimboUsesClock(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	_ = repo.InsertBatch(ctx, txs)

	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}

	limbo, err := repo.FindPendingLimbo(ctx, repository.AnomalyFilter{Window: w}, exp.Now, th)
	if err != nil {
		t.Fatal(err)
	}
	if len(limbo) != exp.AnomalyCounts["pending_limbo"] {
		t.Fatalf("limbo: got %d want %d", len(limbo), exp.AnomalyCounts["pending_limbo"])
	}
	earlyNow := exp.WindowTo.Add(1 * time.Hour)
	limbo2, err := repo.FindPendingLimbo(ctx, repository.AnomalyFilter{Window: w}, earlyNow, th)
	if err != nil {
		t.Fatal(err)
	}
	if len(limbo2) != 0 {
		t.Errorf("limbo with early now should be 0, got %d", len(limbo2))
	}
}

func TestSQLite_InvalidWindowReturnsError(t *testing.T) {
	repo := openMem(t)
	ctx := context.Background()
	from := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	w := repository.Window{From: &from, To: &to}
	_, err := repo.CountsByWindow(ctx, w, time.Now(), repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if !errors.Is(err, domain.ErrInvalidWindow) {
		t.Errorf("expected ErrInvalidWindow, got %v", err)
	}
}

func TestSQLite_EmptyDataset(t *testing.T) {
	repo := openMem(t)
	ctx := context.Background()
	c, err := repo.CountsByWindow(ctx, repository.Window{}, time.Now(), repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if c.Orphaned != 0 || c.Ghost != 0 || c.DuplicateGroups != 0 || c.PendingLimbo != 0 {
		t.Errorf("expected zero counts, got %+v", c)
	}
	uniq, _ := repo.UniqueTransactionIDsCount(ctx, repository.Window{})
	if uniq != 0 {
		t.Errorf("expected 0 unique, got %d", uniq)
	}
}

func TestSQLite_BreakdownByProcessorAndMethod(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	_ = repo.InsertBatch(ctx, txs)
	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}

	byProc, err := repo.CountsByProcessor(ctx, w, exp.Now, th)
	if err != nil {
		t.Fatal(err)
	}
	totals := repository.Counts{}
	for _, c := range byProc {
		totals.Orphaned += c.Orphaned
		totals.Ghost += c.Ghost
		totals.DuplicateGroups += c.DuplicateGroups
		totals.DuplicateExtra += c.DuplicateExtra
		totals.PendingLimbo += c.PendingLimbo
	}
	if totals.Orphaned != exp.AnomalyCounts["orphaned"] {
		t.Errorf("processor breakdown orphaned sum mismatch: %d vs %d", totals.Orphaned, exp.AnomalyCounts["orphaned"])
	}
	if totals.PendingLimbo != exp.AnomalyCounts["pending_limbo"] {
		t.Errorf("processor breakdown limbo sum mismatch: %d vs %d", totals.PendingLimbo, exp.AnomalyCounts["pending_limbo"])
	}

	byPM, err := repo.CountsByPaymentMethod(ctx, w, exp.Now, th)
	if err != nil {
		t.Fatal(err)
	}
	pmLimbo := byPM["pix"].PendingLimbo + byPM["boleto"].PendingLimbo
	if pmLimbo != exp.AnomalyCounts["pending_limbo"] {
		t.Errorf("pix+boleto limbo: %d vs %d", pmLimbo, exp.AnomalyCounts["pending_limbo"])
	}
}
