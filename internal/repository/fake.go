package repository

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
)

// Fake is an in-memory implementation of Repository, useful for service unit
// tests and for experimenting without SQLite. It replicates the window
// semantics documented in ARCHITECTURE.md: the window is applied to the
// "primary side" of each anomaly and the counterpart lookup spans the ENTIRE
// dataset.
type Fake struct {
	mu   sync.RWMutex
	rows []domain.Transaction
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Close() error { return nil }

func (f *Fake) Migrate(_ context.Context) error { return nil }

func (f *Fake) InsertBatch(_ context.Context, txs []domain.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, txs...)
	return nil
}

func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = nil
}

func (f *Fake) snapshot() []domain.Transaction {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]domain.Transaction, len(f.rows))
	copy(out, f.rows)
	return out
}

func inWindow(w Window, t time.Time) bool {
	if w.From != nil && t.Before(*w.From) {
		return false
	}
	if w.To != nil && t.After(*w.To) {
		return false
	}
	return true
}

func paginate[T any](xs []T, limit, offset int) []T {
	if offset >= len(xs) {
		return []T{}
	}
	xs = xs[offset:]
	if limit > 0 && len(xs) > limit {
		xs = xs[:limit]
	}
	return xs
}

func (f *Fake) ListByWindow(_ context.Context, w Window, limit, offset int) ([]domain.Transaction, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	rows := f.snapshot()
	out := make([]domain.Transaction, 0, len(rows))
	for _, r := range rows {
		if inWindow(w, r.OccurredAt) {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return paginate(out, limit, offset), nil
}

func toDetail(t domain.Transaction, reason string) domain.AnomalyDetail {
	return domain.AnomalyDetail{
		TransactionID: t.TransactionID,
		Processor:     t.Processor,
		PaymentMethod: t.PaymentMethod,
		AmountCents:   t.AmountCents,
		Currency:      t.Currency,
		Status:        t.Status,
		Source:        t.Source,
		OccurredAt:    t.OccurredAt,
		Reason:        reason,
	}
}

func (f *Fake) FindOrphanedApprovals(_ context.Context, fl AnomalyFilter) ([]domain.AnomalyDetail, error) {
	if err := fl.Validate(); err != nil {
		return nil, err
	}
	rows := f.snapshot()
	merchantSet := map[string]struct{}{}
	for _, r := range rows {
		if r.Source == domain.SourceMerchantOrderSystem {
			merchantSet[r.TransactionID] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	out := []domain.AnomalyDetail{}
	for _, r := range rows {
		if r.Source != domain.SourceProcessor || r.Status != domain.StatusApproved {
			continue
		}
		if !inWindow(fl.Window, r.OccurredAt) {
			continue
		}
		if _, hasMerchant := merchantSet[r.TransactionID]; hasMerchant {
			continue
		}
		if _, dup := seen[r.TransactionID]; dup {
			continue
		}
		seen[r.TransactionID] = struct{}{}
		out = append(out, toDetail(r, "no merchant order found"))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return paginate(out, fl.Limit, fl.Offset), nil
}

func (f *Fake) FindGhostOrders(_ context.Context, fl AnomalyFilter) ([]domain.AnomalyDetail, error) {
	if err := fl.Validate(); err != nil {
		return nil, err
	}
	rows := f.snapshot()

	type latest struct {
		row domain.Transaction
		set bool
	}
	procLatest := map[string]latest{}
	for _, r := range rows {
		if r.Source != domain.SourceProcessor {
			continue
		}
		cur := procLatest[r.TransactionID]
		if !cur.set || r.OccurredAt.After(cur.row.OccurredAt) {
			procLatest[r.TransactionID] = latest{row: r, set: true}
		}
	}

	seen := map[string]struct{}{}
	out := []domain.AnomalyDetail{}
	for _, r := range rows {
		if r.Source != domain.SourceMerchantOrderSystem || r.Status != domain.StatusApproved {
			continue
		}
		if !inWindow(fl.Window, r.OccurredAt) {
			continue
		}
		if _, dup := seen[r.TransactionID]; dup {
			continue
		}
		proc, hasProc := procLatest[r.TransactionID]
		reason := ""
		if !hasProc {
			reason = "no processor record"
		} else if proc.row.Status == domain.StatusDeclined || proc.row.Status == domain.StatusFailed {
			reason = fmt.Sprintf("processor most-recent status: %s", proc.row.Status)
		} else {
			continue
		}
		seen[r.TransactionID] = struct{}{}
		out = append(out, toDetail(r, reason))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return paginate(out, fl.Limit, fl.Offset), nil
}

func (f *Fake) FindDuplicates(_ context.Context, fl AnomalyFilter) ([]DuplicateGroup, error) {
	if err := fl.Validate(); err != nil {
		return nil, err
	}
	rows := f.snapshot()

	type key struct {
		txID   string
		source domain.Source
	}
	groups := map[key][]domain.Transaction{}
	for _, r := range rows {
		k := key{r.TransactionID, r.Source}
		groups[k] = append(groups[k], r)
	}

	out := []DuplicateGroup{}
	for k, rs := range groups {
		if len(rs) < 2 {
			continue
		}
		hasInWindow := false
		for _, r := range rs {
			if inWindow(fl.Window, r.OccurredAt) {
				hasInWindow = true
				break
			}
		}
		if !hasInWindow {
			continue
		}
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].OccurredAt.Before(rs[j].OccurredAt) })
		details := make([]domain.AnomalyDetail, 0, len(rs))
		for _, r := range rs {
			details = append(details, toDetail(r, fmt.Sprintf("duplicate group of %d", len(rs))))
		}
		out = append(out, DuplicateGroup{
			TransactionID: k.txID,
			Source:        k.source,
			Occurrences:   len(rs),
			Rows:          details,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TransactionID != out[j].TransactionID {
			return out[i].TransactionID < out[j].TransactionID
		}
		return out[i].Source < out[j].Source
	})
	return paginate(out, fl.Limit, fl.Offset), nil
}

func (f *Fake) FindPendingLimbo(_ context.Context, fl AnomalyFilter, now time.Time, th PendingLimboThresholds) ([]domain.AnomalyDetail, error) {
	if err := fl.Validate(); err != nil {
		return nil, err
	}
	rows := f.snapshot()
	out := []domain.AnomalyDetail{}
	for _, r := range rows {
		if r.Source != domain.SourceProcessor || r.Status != domain.StatusPending {
			continue
		}
		if !inWindow(fl.Window, r.OccurredAt) {
			continue
		}
		var threshold time.Duration
		switch r.PaymentMethod {
		case domain.PaymentMethodPix:
			threshold = th.Pix
		case domain.PaymentMethodBoleto:
			threshold = th.Boleto
		default:
			continue
		}
		if now.Sub(r.OccurredAt) <= threshold {
			continue
		}
		reason := fmt.Sprintf("pending %s exceeds %s threshold", r.PaymentMethod, threshold)
		out = append(out, toDetail(r, reason))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].OccurredAt.Before(out[j].OccurredAt) })
	return paginate(out, fl.Limit, fl.Offset), nil
}

func (f *Fake) CountsByWindow(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (Counts, error) {
	if err := w.Validate(); err != nil {
		return Counts{}, err
	}
	all := AnomalyFilter{Window: w}
	orph, err := f.FindOrphanedApprovals(ctx, all)
	if err != nil {
		return Counts{}, err
	}
	ghost, err := f.FindGhostOrders(ctx, all)
	if err != nil {
		return Counts{}, err
	}
	dups, err := f.FindDuplicates(ctx, all)
	if err != nil {
		return Counts{}, err
	}
	limbo, err := f.FindPendingLimbo(ctx, all, now, th)
	if err != nil {
		return Counts{}, err
	}

	dupExtra := 0
	for _, g := range dups {
		dupExtra += g.Occurrences - 1
	}

	uniq, err := f.UniqueTransactionIDsCount(ctx, w)
	if err != nil {
		return Counts{}, err
	}
	total := 0
	for _, r := range f.snapshot() {
		if inWindow(w, r.OccurredAt) {
			total++
		}
	}
	return Counts{
		Orphaned:        len(orph),
		Ghost:           len(ghost),
		DuplicateGroups: len(dups),
		DuplicateExtra:  dupExtra,
		PendingLimbo:    len(limbo),
		UniqueInWindow:  uniq,
		TotalRowsWindow: total,
	}, nil
}

func (f *Fake) CountsByProcessor(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (map[string]Counts, error) {
	return f.countsByKey(ctx, w, now, th, func(d domain.AnomalyDetail) string { return d.Processor })
}

func (f *Fake) CountsByPaymentMethod(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (map[string]Counts, error) {
	return f.countsByKey(ctx, w, now, th, func(d domain.AnomalyDetail) string { return string(d.PaymentMethod) })
}

func (f *Fake) countsByKey(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds, key func(domain.AnomalyDetail) string) (map[string]Counts, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	out := map[string]Counts{}

	bump := func(k string, mut func(*Counts)) {
		c := out[k]
		mut(&c)
		out[k] = c
	}

	all := AnomalyFilter{Window: w}
	orph, _ := f.FindOrphanedApprovals(ctx, all)
	for _, d := range orph {
		bump(key(d), func(c *Counts) { c.Orphaned++ })
	}
	ghost, _ := f.FindGhostOrders(ctx, all)
	for _, d := range ghost {
		bump(key(d), func(c *Counts) { c.Ghost++ })
	}
	dups, _ := f.FindDuplicates(ctx, all)
	for _, g := range dups {
		if len(g.Rows) == 0 {
			continue
		}
		k := key(g.Rows[0])
		bump(k, func(c *Counts) {
			c.DuplicateGroups++
			c.DuplicateExtra += g.Occurrences - 1
		})
	}
	limbo, _ := f.FindPendingLimbo(ctx, all, now, th)
	for _, d := range limbo {
		bump(key(d), func(c *Counts) { c.PendingLimbo++ })
	}
	return out, nil
}

func (f *Fake) UniqueTransactionIDsCount(_ context.Context, w Window) (int, error) {
	if err := w.Validate(); err != nil {
		return 0, err
	}
	uniq := map[string]struct{}{}
	for _, r := range f.snapshot() {
		if inWindow(w, r.OccurredAt) {
			uniq[r.TransactionID] = struct{}{}
		}
	}
	return len(uniq), nil
}
