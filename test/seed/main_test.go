package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func generateForTest(t *testing.T, total int, seed int64) ([]domain.Transaction, ExpectedCounts) {
	t.Helper()
	from := mustParse(t, BaseWindowStart)
	to := mustParse(t, BaseWindowEnd)
	now := mustParse(t, BaseNow)
	rng := rand.New(rand.NewSource(seed))
	pairs := (total - AnomalyRows + 1) / 2
	if pairs < 1 {
		pairs = 1
	}
	txs := generate(rng, pairs, from, to)
	exp := buildExpected(txs, from, to, now, pairs)
	return txs, exp
}

func TestGenerate_TotalRowsAtLeast500(t *testing.T) {
	txs, _ := generateForTest(t, 500, 42)
	if len(txs) < 500 {
		t.Fatalf("expected >=500 transactions, got %d", len(txs))
	}
}

func TestGenerate_AnomaliesDetectableTrivially(t *testing.T) {
	txs, exp := generateForTest(t, 500, 42)
	from, to := mustParse(t, BaseWindowStart), mustParse(t, BaseWindowEnd)

	type pmKey struct {
		txID   string
		source domain.Source
	}

	groupsBySrc := map[pmKey]int{}
	procApproved := map[string]int{}
	merchApproved := map[string]int{}
	procStatusLatest := map[string]struct {
		status domain.Status
		ts     time.Time
	}{}
	limbo := 0

	for _, tx := range txs {
		groupsBySrc[pmKey{tx.TransactionID, tx.Source}]++
		if tx.Source == domain.SourceProcessor && tx.Status == domain.StatusApproved {
			procApproved[tx.TransactionID]++
		}
		if tx.Source == domain.SourceMerchantOrderSystem && tx.Status == domain.StatusApproved {
			merchApproved[tx.TransactionID]++
		}
		if tx.Source == domain.SourceProcessor {
			cur, ok := procStatusLatest[tx.TransactionID]
			if !ok || tx.OccurredAt.After(cur.ts) {
				procStatusLatest[tx.TransactionID] = struct {
					status domain.Status
					ts     time.Time
				}{tx.Status, tx.OccurredAt}
			}
		}
		inWindow := !tx.OccurredAt.Before(from) && !tx.OccurredAt.After(to)
		if inWindow && tx.Source == domain.SourceProcessor && tx.Status == domain.StatusPending &&
			(tx.PaymentMethod == domain.PaymentMethodPix || tx.PaymentMethod == domain.PaymentMethodBoleto) {
			limbo++
		}
	}

	orphaned := 0
	for txID := range procApproved {
		if !strings.HasPrefix(txID, "ORPHAN-") {
			continue
		}
		if _, hasMerch := merchApproved[txID]; !hasMerch {
			orphaned++
		}
	}
	if orphaned != NumOrphaned {
		t.Errorf("orphaned: expected %d, got %d", NumOrphaned, orphaned)
	}

	ghost := 0
	for txID := range merchApproved {
		if !strings.HasPrefix(txID, "GHOST-") {
			continue
		}
		latest, hasProc := procStatusLatest[txID]
		if !hasProc {
			ghost++
			continue
		}
		if latest.status == domain.StatusDeclined || latest.status == domain.StatusFailed {
			ghost++
		}
	}
	if ghost != NumGhostNoProc+NumGhostBadProc {
		t.Errorf("ghost: expected %d, got %d", NumGhostNoProc+NumGhostBadProc, ghost)
	}

	dupGroups := 0
	dupExtras := 0
	for k, n := range groupsBySrc {
		if n > 1 && (strings.HasPrefix(k.txID, "DUP-PROC-") || strings.HasPrefix(k.txID, "DUP-MERCH-")) {
			dupGroups++
			dupExtras += n - 1
		}
	}
	if dupGroups != NumDupGroupsLg+NumDupGroupsSm {
		t.Errorf("duplicate groups: expected %d, got %d", NumDupGroupsLg+NumDupGroupsSm, dupGroups)
	}
	if dupExtras != exp.DuplicateExtraRows {
		t.Errorf("duplicate extra rows: expected %d, got %d", exp.DuplicateExtraRows, dupExtras)
	}

	if limbo != NumPendingLimbo {
		t.Errorf("pending limbo: expected %d, got %d", NumPendingLimbo, limbo)
	}
}

func TestGenerate_DeterministicWithSameSeed(t *testing.T) {
	a, _ := generateForTest(t, 500, 42)
	b, _ := generateForTest(t, 500, 42)
	if len(a) != len(b) {
		t.Fatalf("len mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d diverges: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestGenerate_AllInWindow(t *testing.T) {
	txs, _ := generateForTest(t, 500, 42)
	from, to := mustParse(t, BaseWindowStart), mustParse(t, BaseWindowEnd)
	for _, tx := range txs {
		if tx.OccurredAt.Before(from) || tx.OccurredAt.After(to) {
			t.Fatalf("tx %s occurred_at %s outside window [%s,%s]", tx.TransactionID, tx.OccurredAt, from, to)
		}
	}
}

func TestGenerate_PendingLimboMix(t *testing.T) {
	txs, _ := generateForTest(t, 500, 42)
	pix, boleto := 0, 0
	for _, tx := range txs {
		if !strings.HasPrefix(tx.TransactionID, "LIMBO-") {
			continue
		}
		if tx.Source != domain.SourceProcessor || tx.Status != domain.StatusPending {
			t.Fatalf("LIMBO row not processor/pending: %+v", tx)
		}
		switch tx.PaymentMethod {
		case domain.PaymentMethodPix:
			pix++
		case domain.PaymentMethodBoleto:
			boleto++
		default:
			t.Fatalf("LIMBO row with unexpected payment method %s", tx.PaymentMethod)
		}
	}
	if pix != PixLimboCount {
		t.Errorf("pix limbo: expected %d, got %d", PixLimboCount, pix)
	}
	if boleto != BoletoLimboCount {
		t.Errorf("boleto limbo: expected %d, got %d", BoletoLimboCount, boleto)
	}
}

func TestGenerate_AllValid(t *testing.T) {
	txs, _ := generateForTest(t, 500, 42)
	for i, tx := range txs {
		if err := tx.Validate(); err != nil {
			t.Fatalf("row %d invalid: %v (%+v)", i, err, tx)
		}
	}
}

func TestExpectedCounts_Healthy(t *testing.T) {
	_, exp := generateForTest(t, 500, 42)
	if exp.AnomalyCounts["orphaned"] != NumOrphaned {
		t.Errorf("orphaned count")
	}
	if exp.AnomalyCounts["ghost"] != NumGhostNoProc+NumGhostBadProc {
		t.Errorf("ghost count")
	}
	if exp.AnomalyCounts["duplicate"] != NumDupGroupsLg+NumDupGroupsSm {
		t.Errorf("duplicate groups count")
	}
	if exp.AnomalyCounts["pending_limbo"] != NumPendingLimbo {
		t.Errorf("pending_limbo count")
	}
	if exp.HealthScore <= 0 || exp.HealthScore > 1 {
		t.Errorf("health score out of range: %v", exp.HealthScore)
	}
}
