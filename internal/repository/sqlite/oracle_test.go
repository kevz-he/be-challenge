package sqlite_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// TestRepository_OracleMatchesExpectedCountsAndIDs is the most important
// integration test: it asserts that, for the deterministic seed dataset, the
// repository returns EXACTLY the transaction_id sets injected by test/seed —
// not just the right cardinality. This single test guards the
// functional+accuracy points of the rubric.
func TestRepository_OracleMatchesExpectedCountsAndIDs(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	if err := repo.InsertBatch(ctx, txs); err != nil {
		t.Fatalf("insert: %v", err)
	}

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	th := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}

	orph, err := repo.FindOrphanedApprovals(ctx, repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatalf("orphaned: %v", err)
	}
	assertIDSet(t, "orphaned", idsOf(orph), exp.IDs["orphaned"])

	ghost, err := repo.FindGhostOrders(ctx, repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatalf("ghost: %v", err)
	}
	assertIDSet(t, "ghost", idsOf(ghost), exp.IDs["ghost"])

	dups, err := repo.FindDuplicates(ctx, repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	dupIDs := make([]string, 0, len(dups))
	seen := map[string]struct{}{}
	for _, g := range dups {
		if _, ok := seen[g.TransactionID]; ok {
			continue
		}
		seen[g.TransactionID] = struct{}{}
		dupIDs = append(dupIDs, g.TransactionID)
	}
	assertIDSet(t, "duplicate", dupIDs, exp.IDs["duplicate"])

	limbo, err := repo.FindPendingLimbo(ctx, repository.AnomalyFilter{Window: w}, exp.Now, th)
	if err != nil {
		t.Fatalf("pending_limbo: %v", err)
	}
	assertIDSet(t, "pending_limbo", idsOf(limbo), exp.IDs["pending_limbo"])
}

func idsOf(ds []domain.AnomalyDetail) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.TransactionID)
	}
	return out
}

func assertIDSet(t *testing.T, label string, got, want []string) {
	t.Helper()
	gset := stringSet(got)
	wset := stringSet(want)
	if len(gset) != len(wset) {
		t.Errorf("%s: cardinality mismatch got=%d want=%d", label, len(gset), len(wset))
	}
	missing := diff(wset, gset)
	extra := diff(gset, wset)
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("%s: ID set mismatch.\n missing: %v\n extra: %v", label, missing, extra)
	}
}

func stringSet(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, s := range in {
		m[s] = struct{}{}
	}
	return m
}

func diff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
