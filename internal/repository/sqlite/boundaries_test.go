package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
)

// helpers ---------------------------------------------------------------

func ptrTime(t time.Time) *time.Time { return &t }

func tx(txID string, src domain.Source, status domain.Status, pm domain.PaymentMethod, occurred time.Time) domain.Transaction {
	return domain.Transaction{
		TransactionID: txID,
		OccurredAt:    occurred.UTC(),
		AmountCents:   1234,
		Currency:      domain.CurrencyBRL,
		PaymentMethod: pm,
		Processor:     "stripe_br",
		Status:        status,
		Source:        src,
	}
}

func insert(t *testing.T, repo *sqlite.Repo, txs ...domain.Transaction) {
	t.Helper()
	if err := repo.InsertBatch(context.Background(), txs); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// Window boundaries -----------------------------------------------------

func TestRepository_WindowBoundaries_FromAndToInclusive(t *testing.T) {
	repo := openMem(t)
	from := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 6, 0, 0, 0, time.UTC)

	insert(t, repo,
		tx("AT-FROM", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, from),
		tx("AT-TO", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, to),
	)
	w := repository.Window{From: &from, To: &to}
	res, err := repo.FindOrphanedApprovals(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 orphans (from+to inclusive), got %d", len(res))
	}
}

func TestRepository_WindowBoundaries_OneNanoOutsideExcluded(t *testing.T) {
	repo := openMem(t)
	from := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 6, 0, 0, 0, time.UTC)

	insert(t, repo,
		tx("BEFORE", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, from.Add(-time.Nanosecond)),
		tx("AFTER", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, to.Add(time.Nanosecond)),
	)
	w := repository.Window{From: &from, To: &to}
	res, err := repo.FindOrphanedApprovals(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("expected 0 orphans (1ns outside excluded), got %d: %+v", len(res), res)
	}
}

// Critical: counterpart lookup must scan the whole dataset, not the window.
// processor 23:59 inside window + merchant 00:01 outside window must NOT be
// reported as orphaned.
func TestRepository_WindowBoundaries_CounterpartOutsideWindowNoFalsePositive(t *testing.T) {
	repo := openMem(t)
	from := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 23, 59, 0, 0, time.UTC)
	procTime := time.Date(2026, 4, 15, 23, 59, 0, 0, time.UTC)
	merchTime := time.Date(2026, 4, 16, 0, 1, 0, 0, time.UTC)

	insert(t, repo,
		tx("X1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, procTime),
		tx("X1", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, merchTime),
	)
	w := repository.Window{From: &from, To: &to}
	res, err := repo.FindOrphanedApprovals(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("processor inside window with merchant outside should not orphan, got %d: %+v", len(res), res)
	}
}

func TestRepository_EmptyWindow_ScoreOne(t *testing.T) {
	repo := openMem(t)
	c, err := repo.CountsByWindow(context.Background(), repository.Window{}, time.Now(), repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if c.UniqueInWindow != 0 || c.Orphaned != 0 || c.Ghost != 0 || c.DuplicateGroups != 0 || c.PendingLimbo != 0 {
		t.Fatalf("expected all-zero counts, got %+v", c)
	}
}

// Duplicate semantics ----------------------------------------------------

func TestRepository_DuplicateSemantics_KeyIsTransactionIdAndSource(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)

	// 2 rows in processor and 2 rows in merchant for the same transaction_id.
	// Should produce TWO duplicate groups (one per source), not one.
	insert(t, repo,
		tx("D1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base),
		tx("D1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(time.Second)),
		tx("D1", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(2*time.Second)),
		tx("D1", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(3*time.Second)),
	)
	groups, err := repo.FindDuplicates(context.Background(), repository.AnomalyFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 duplicate groups (one per source), got %d: %+v", len(groups), groups)
	}
	bySource := map[domain.Source]int{}
	for _, g := range groups {
		bySource[g.Source] = g.Occurrences
	}
	if bySource[domain.SourceProcessor] != 2 || bySource[domain.SourceMerchantOrderSystem] != 2 {
		t.Errorf("expected occurrences {processor:2, merchant:2}, got %+v", bySource)
	}
}

func TestRepository_DuplicateSemantics_OneRowPerSourceNotDuplicate(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		tx("D2", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base),
		tx("D2", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(time.Second)),
	)
	groups, err := repo.FindDuplicates(context.Background(), repository.AnomalyFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("same tx_id once per source must not be duplicate, got %d", len(groups))
	}
}

func TestRepository_DuplicateSemantics_GroupOf3HasExtras2(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		tx("D3", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base),
		tx("D3", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(time.Second)),
		tx("D3", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(2*time.Second)),
		// merchant counterpart so the group is not orphaned
		tx("D3", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(3*time.Second)),
	)
	c, err := repo.CountsByWindow(context.Background(), repository.Window{}, time.Now(), repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if c.DuplicateGroups != 1 {
		t.Errorf("groups: got %d want 1", c.DuplicateGroups)
	}
	if c.DuplicateExtra != 2 {
		t.Errorf("extra: got %d want 2", c.DuplicateExtra)
	}
}

func TestRepository_DuplicateSemantics_WindowMembership(t *testing.T) {
	repo := openMem(t)
	from := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 2, 0, 0, 0, time.UTC)
	insideA := time.Date(2026, 4, 15, 1, 30, 0, 0, time.UTC)
	insideB := time.Date(2026, 4, 15, 1, 45, 0, 0, time.UTC)
	outside := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)

	// A duplicate group fully inside the window: must be reported.
	insert(t, repo,
		tx("DW-IN", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, insideA),
		tx("DW-IN", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, insideB),
	)
	// A duplicate group fully outside the window: must NOT be reported.
	insert(t, repo,
		tx("DW-OUT", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, outside),
		tx("DW-OUT", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, outside.Add(time.Second)),
	)
	w := repository.Window{From: &from, To: &to}
	groups, err := repo.FindDuplicates(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].TransactionID != "DW-IN" {
		t.Fatalf("expected only DW-IN, got %+v", groups)
	}
}

// Pending limbo boundaries ---------------------------------------------

func limboTh() repository.PendingLimboThresholds {
	return repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
}

func TestRepository_PendingLimbo_PixExactly24hDoesNotCount(t *testing.T) {
	repo := openMem(t)
	occurred := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	now := occurred.Add(24 * time.Hour)

	insert(t, repo, tx("PIX-EQ", domain.SourceProcessor, domain.StatusPending, domain.PaymentMethodPix, occurred))
	res, err := repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, now, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Fatalf("PIX exactly at 24h should not count (strict >), got %d", len(res))
	}
}

func TestRepository_PendingLimbo_PixOneNanoPast24hCounts(t *testing.T) {
	repo := openMem(t)
	occurred := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	now := occurred.Add(24*time.Hour + time.Nanosecond)

	insert(t, repo, tx("PIX-GT", domain.SourceProcessor, domain.StatusPending, domain.PaymentMethodPix, occurred))
	res, err := repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, now, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("PIX 24h+1ns should count, got %d", len(res))
	}
}

func TestRepository_PendingLimbo_BoletoBoundaries(t *testing.T) {
	repo := openMem(t)
	occurred := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	insert(t, repo, tx("BOL-1", domain.SourceProcessor, domain.StatusPending, domain.PaymentMethodBoleto, occurred))

	exact := occurred.Add(72 * time.Hour)
	res, err := repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, exact, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("boleto exactly at 72h should not count, got %d", len(res))
	}
	past := occurred.Add(72*time.Hour + time.Nanosecond)
	res, err = repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, past, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Errorf("boleto 72h+1ns should count, got %d", len(res))
	}
}

func TestRepository_PendingLimbo_CreditCardNeverCounts(t *testing.T) {
	repo := openMem(t)
	occurred := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) // 30 days later

	insert(t, repo, tx("CC-PEND", domain.SourceProcessor, domain.StatusPending, domain.PaymentMethodCreditCard, occurred))
	res, err := repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, now, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("credit_card pending must NEVER count as limbo, got %d", len(res))
	}
}

func TestRepository_PendingLimbo_MerchantPendingNeverCounts(t *testing.T) {
	repo := openMem(t)
	occurred := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	insert(t, repo, tx("M-PEND", domain.SourceMerchantOrderSystem, domain.StatusPending, domain.PaymentMethodPix, occurred))
	res, err := repo.FindPendingLimbo(context.Background(), repository.AnomalyFilter{}, now, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("merchant_order_system pending must NEVER count as limbo (only processor), got %d", len(res))
	}
}

// Multi-category --------------------------------------------------------

// A transaction may legitimately appear in multiple anomaly buckets at once.
// Here we engineer one that is BOTH orphaned (processor approved, no
// merchant) AND a duplicate group (3 processor rows).
func TestRepository_MultiCategory_OrphanedAndDuplicate(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		tx("MC1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base),
		tx("MC1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(time.Second)),
		tx("MC1", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(2*time.Second)),
	)
	w := repository.Window{}
	orph, _ := repo.FindOrphanedApprovals(context.Background(), repository.AnomalyFilter{Window: w})
	dups, _ := repo.FindDuplicates(context.Background(), repository.AnomalyFilter{Window: w})
	if len(orph) != 1 || orph[0].TransactionID != "MC1" {
		t.Errorf("expected MC1 in orphaned, got %+v", orph)
	}
	if len(dups) != 1 || dups[0].TransactionID != "MC1" {
		t.Errorf("expected MC1 in duplicate, got %+v", dups)
	}
}

// Ghost: latest processor status -------------------------------------

// processor declined then approved: ghost detection must use the LATEST
// status (approved) → not ghost.
func TestRepository_Ghost_LatestProcessorStatusApprovedNotGhost(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		tx("G-OK", domain.SourceProcessor, domain.StatusDeclined, domain.PaymentMethodCreditCard, base),
		tx("G-OK", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(time.Minute)),
		tx("G-OK", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(2*time.Minute)),
	)
	w := repository.Window{}
	ghost, err := repo.FindGhostOrders(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(ghost) != 0 {
		t.Errorf("latest approved must not be ghost, got %+v", ghost)
	}
}

// processor approved then declined: ghost via latest=declined.
func TestRepository_Ghost_LatestProcessorStatusDeclinedIsGhost(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		tx("G-BAD", domain.SourceProcessor, domain.StatusApproved, domain.PaymentMethodCreditCard, base),
		tx("G-BAD", domain.SourceProcessor, domain.StatusDeclined, domain.PaymentMethodCreditCard, base.Add(time.Minute)),
		tx("G-BAD", domain.SourceMerchantOrderSystem, domain.StatusApproved, domain.PaymentMethodCreditCard, base.Add(2*time.Minute)),
	)
	w := repository.Window{}
	ghost, err := repo.FindGhostOrders(context.Background(), repository.AnomalyFilter{Window: w})
	if err != nil {
		t.Fatal(err)
	}
	if len(ghost) != 1 || ghost[0].TransactionID != "G-BAD" {
		t.Errorf("expected G-BAD ghost via latest declined, got %+v", ghost)
	}
}

// Same tx_id with different amounts across sources: not duplicate, not
// orphaned (counterpart exists).
func TestRepository_SameTxIdDifferentAmountsNotAnomaly(t *testing.T) {
	repo := openMem(t)
	base := time.Date(2026, 4, 15, 1, 0, 0, 0, time.UTC)
	insert(t, repo,
		domain.Transaction{
			TransactionID: "AMT", OccurredAt: base, AmountCents: 100, Currency: domain.CurrencyBRL,
			PaymentMethod: domain.PaymentMethodCreditCard, Processor: "stripe_br",
			Status: domain.StatusApproved, Source: domain.SourceProcessor,
		},
		domain.Transaction{
			TransactionID: "AMT", OccurredAt: base.Add(time.Second), AmountCents: 999, Currency: domain.CurrencyBRL,
			PaymentMethod: domain.PaymentMethodCreditCard, Processor: "stripe_br",
			Status: domain.StatusApproved, Source: domain.SourceMerchantOrderSystem,
		},
	)
	c, err := repo.CountsByWindow(context.Background(), repository.Window{}, time.Now(), limboTh())
	if err != nil {
		t.Fatal(err)
	}
	if c.Orphaned != 0 || c.DuplicateGroups != 0 || c.Ghost != 0 {
		t.Errorf("amount mismatch should not raise an anomaly in the MVP, got %+v", c)
	}
}

// HealthScore matches oracle ------------------------------------------

func TestRepository_HealthScoreMatchesOracle(t *testing.T) {
	txs, exp := loadFixturesAndOracle(t)
	repo := openMem(t)
	ctx := context.Background()
	if err := repo.InsertBatch(ctx, txs); err != nil {
		t.Fatal(err)
	}
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	c, err := repo.CountsByWindow(ctx, w, exp.Now, limboTh())
	if err != nil {
		t.Fatal(err)
	}
	denom := c.UniqueInWindow
	anomalies := c.Orphaned + c.Ghost + c.DuplicateExtra + c.PendingLimbo
	score := 1.0
	if denom > 0 {
		score = 1.0 - float64(anomalies)/float64(denom)
	}
	want := exp.HealthScore
	if exp.ExpectedHealthScore != 0 {
		want = exp.ExpectedHealthScore
	}
	if absf(score-want) > 1e-9 {
		t.Errorf("health score: got %.12f want %.12f", score, want)
	}
}

func absf(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// just to satisfy the import if we ever drop a usage
var _ = ptrTime
