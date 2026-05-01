package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

func (r *Repo) InsertBatch(ctx context.Context, txs []domain.Transaction) error {
	if len(txs) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	const stmt = `INSERT INTO transactions
        (transaction_id, occurred_at, amount_cents, currency, payment_method, processor, status, source)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	prepared, err := tx.PrepareContext(ctx, stmt)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer prepared.Close()

	for i, t := range txs {
		if _, err := prepared.ExecContext(ctx,
			t.TransactionID,
			formatTime(t.OccurredAt),
			t.AmountCents,
			t.Currency,
			string(t.PaymentMethod),
			t.Processor,
			string(t.Status),
			string(t.Source),
		); err != nil {
			return fmt.Errorf("insert row %d (%s): %w", i, t.TransactionID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch: %w", err)
	}
	return nil
}

// windowClause builds the window filter over the alias.col column.
// If both from and to are nil, it returns an empty clause.
func windowClause(alias, col string, w repository.Window) (string, []any) {
	parts := []string{}
	args := []any{}
	if w.From != nil {
		parts = append(parts, fmt.Sprintf("%s.%s >= ?", alias, col))
		args = append(args, formatTime(*w.From))
	}
	if w.To != nil {
		parts = append(parts, fmt.Sprintf("%s.%s <= ?", alias, col))
		args = append(args, formatTime(*w.To))
	}
	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, " AND "), args
}

// limitOffsetClause generates "LIMIT ? OFFSET ?" when limit > 0.
func limitOffsetClause(limit, offset int) (string, []any) {
	if limit <= 0 {
		return "", nil
	}
	return " LIMIT ? OFFSET ?", []any{limit, offset}
}

func scanDetail(rs *sql.Rows, reason string) (domain.AnomalyDetail, error) {
	var (
		txID, currency, processor                 string
		paymentMethod, status, source, occurredAt string
		amount                                    int64
	)
	if err := rs.Scan(&txID, &processor, &paymentMethod, &amount, &currency, &status, &source, &occurredAt); err != nil {
		return domain.AnomalyDetail{}, err
	}
	t, err := parseTime(occurredAt)
	if err != nil {
		return domain.AnomalyDetail{}, err
	}
	return domain.AnomalyDetail{
		TransactionID: txID,
		Processor:     processor,
		PaymentMethod: domain.PaymentMethod(paymentMethod),
		AmountCents:   amount,
		Currency:      currency,
		Status:        domain.Status(status),
		Source:        domain.Source(source),
		OccurredAt:    t,
		Reason:        reason,
	}, nil
}

func (r *Repo) ListByWindow(ctx context.Context, w repository.Window, limit, offset int) ([]domain.Transaction, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	q := `SELECT transaction_id, occurred_at, amount_cents, currency, payment_method, processor, status, source FROM transactions t`
	args := []any{}
	if where, wArgs := windowClause("t", "occurred_at", w); where != "" {
		q += " WHERE " + where
		args = append(args, wArgs...)
	}
	q += " ORDER BY t.occurred_at ASC, t.id ASC"
	if lo, lArgs := limitOffsetClause(limit, offset); lo != "" {
		q += lo
		args = append(args, lArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list by window: %w", err)
	}
	defer rs.Close()

	out := []domain.Transaction{}
	for rs.Next() {
		var (
			txID, currency, processor                 string
			paymentMethod, status, source, occurredAt string
			amount                                    int64
		)
		if err := rs.Scan(&txID, &occurredAt, &amount, &currency, &paymentMethod, &processor, &status, &source); err != nil {
			return nil, err
		}
		t, err := parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.Transaction{
			TransactionID: txID,
			OccurredAt:    t,
			AmountCents:   amount,
			Currency:      currency,
			PaymentMethod: domain.PaymentMethod(paymentMethod),
			Processor:     processor,
			Status:        domain.Status(status),
			Source:        domain.Source(source),
		})
	}
	return out, rs.Err()
}

// FindOrphanedApprovals: processor approved rows whose transaction_id does
// NOT appear in merchant_order_system (across the ENTIRE dataset). The window
// filter is applied only to the processor side.
func (r *Repo) FindOrphanedApprovals(ctx context.Context, f repository.AnomalyFilter) ([]domain.AnomalyDetail, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	q := `SELECT p.transaction_id, p.processor, p.payment_method, p.amount_cents,
                 p.currency, p.status, p.source, MIN(p.occurred_at) AS occurred_at
          FROM transactions p
          WHERE p.source = 'processor' AND p.status = 'approved'`
	args := []any{}
	if where, wArgs := windowClause("p", "occurred_at", f.Window); where != "" {
		q += " AND " + where
		args = append(args, wArgs...)
	}
	q += `   AND NOT EXISTS (
            SELECT 1 FROM transactions m
            WHERE m.source = 'merchant_order_system'
              AND m.transaction_id = p.transaction_id
          )
          GROUP BY p.transaction_id
          ORDER BY occurred_at ASC, p.transaction_id ASC`
	if lo, lArgs := limitOffsetClause(f.Limit, f.Offset); lo != "" {
		q += lo
		args = append(args, lArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("find orphaned: %w", err)
	}
	defer rs.Close()

	out := []domain.AnomalyDetail{}
	for rs.Next() {
		d, err := scanDetail(rs, "no merchant order found")
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rs.Err()
}

// FindGhostOrders: merchant approved rows in window with no processor record,
// or whose latest processor status (MAX(occurred_at)) is declined or failed.
func (r *Repo) FindGhostOrders(ctx context.Context, f repository.AnomalyFilter) ([]domain.AnomalyDetail, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	// proc_latest exposes EXACTLY ONE row per processor-side transaction_id:
	// the most recent occurrence by occurred_at, with id DESC as a stable
	// tiebreak when two processor rows share the same timestamp. Without
	// the tiebreak, a JOIN against MAX(occurred_at) would emit BOTH rows
	// on a tie, and a single conflicting status (e.g. "approved" + "declined"
	// at the same nanosecond) would silently be reported as ghost via the
	// declined branch. ROW_NUMBER() collapses the tie deterministically.
	q := `WITH proc_latest AS (
            SELECT transaction_id, status
            FROM (
              SELECT transaction_id, status,
                     ROW_NUMBER() OVER (
                       PARTITION BY transaction_id
                       ORDER BY occurred_at DESC, id DESC
                     ) AS rn
              FROM transactions
              WHERE source = 'processor'
            )
            WHERE rn = 1
          )
          SELECT m.transaction_id, m.processor, m.payment_method, m.amount_cents,
                 m.currency, m.status, m.source, MIN(m.occurred_at) AS occurred_at,
                 COALESCE(p.status, '') AS proc_status
          FROM transactions m
          LEFT JOIN proc_latest p ON p.transaction_id = m.transaction_id
          WHERE m.source = 'merchant_order_system' AND m.status = 'approved'`
	args := []any{}
	if where, wArgs := windowClause("m", "occurred_at", f.Window); where != "" {
		q += " AND " + where
		args = append(args, wArgs...)
	}
	q += `   AND (p.status IS NULL OR p.status IN ('declined','failed'))
          GROUP BY m.transaction_id
          ORDER BY occurred_at ASC, m.transaction_id ASC`
	if lo, lArgs := limitOffsetClause(f.Limit, f.Offset); lo != "" {
		q += lo
		args = append(args, lArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("find ghost: %w", err)
	}
	defer rs.Close()

	out := []domain.AnomalyDetail{}
	for rs.Next() {
		var (
			txID, currency, processor                 string
			paymentMethod, status, source, occurredAt string
			procStatus                                string
			amount                                    int64
		)
		if err := rs.Scan(&txID, &processor, &paymentMethod, &amount, &currency, &status, &source, &occurredAt, &procStatus); err != nil {
			return nil, err
		}
		t, err := parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		reason := "no processor record"
		if procStatus != "" {
			reason = "processor most-recent status: " + procStatus
		}
		out = append(out, domain.AnomalyDetail{
			TransactionID: txID,
			Processor:     processor,
			PaymentMethod: domain.PaymentMethod(paymentMethod),
			AmountCents:   amount,
			Currency:      currency,
			Status:        domain.Status(status),
			Source:        domain.Source(source),
			OccurredAt:    t,
			Reason:        reason,
		})
	}
	return out, rs.Err()
}

// FindDuplicates: (transaction_id, source) groups with count > 1, where at
// least ONE row of the group falls in the window. The group detail includes
// all of its rows (not filtered by window).
func (r *Repo) FindDuplicates(ctx context.Context, f repository.AnomalyFilter) ([]repository.DuplicateGroup, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	q := `SELECT transaction_id, source, COUNT(*) AS cnt
          FROM transactions
          GROUP BY transaction_id, source
          HAVING COUNT(*) > 1`
	args := []any{}
	if where, wArgs := windowClause("transactions", "occurred_at", f.Window); where != "" {
		q = `SELECT transaction_id, source, COUNT(*) AS cnt
             FROM transactions
             GROUP BY transaction_id, source
             HAVING COUNT(*) > 1
                AND EXISTS (
                  SELECT 1 FROM transactions inner_t
                  WHERE inner_t.transaction_id = transactions.transaction_id
                    AND inner_t.source = transactions.source
                    AND ` + strings.ReplaceAll(where, "transactions.", "inner_t.") + `
                )`
		args = append(args, wArgs...)
	}
	q += ` ORDER BY transaction_id ASC, source ASC`
	if lo, lArgs := limitOffsetClause(f.Limit, f.Offset); lo != "" {
		q += lo
		args = append(args, lArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("find duplicates: %w", err)
	}

	type groupKey struct {
		txID   string
		source string
		count  int
	}
	keys := []groupKey{}
	for rs.Next() {
		var k groupKey
		if err := rs.Scan(&k.txID, &k.source, &k.count); err != nil {
			rs.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	rs.Close()

	out := make([]repository.DuplicateGroup, 0, len(keys))
	for _, k := range keys {
		rows, err := r.fetchGroupRows(ctx, k.txID, k.source, k.count)
		if err != nil {
			return nil, err
		}
		out = append(out, repository.DuplicateGroup{
			TransactionID: k.txID,
			Source:        domain.Source(k.source),
			Occurrences:   k.count,
			Rows:          rows,
		})
	}
	return out, nil
}

func (r *Repo) fetchGroupRows(ctx context.Context, txID, source string, count int) ([]domain.AnomalyDetail, error) {
	q := `SELECT transaction_id, processor, payment_method, amount_cents,
                 currency, status, source, occurred_at
          FROM transactions
          WHERE transaction_id = ? AND source = ?
          ORDER BY occurred_at ASC, id ASC`
	rs, err := r.db.QueryContext(ctx, q, txID, source)
	if err != nil {
		return nil, fmt.Errorf("fetch group rows: %w", err)
	}
	defer rs.Close()
	reason := fmt.Sprintf("duplicate group of %d", count)
	out := make([]domain.AnomalyDetail, 0, count)
	for rs.Next() {
		d, err := scanDetail(rs, reason)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rs.Err()
}

// FindPendingLimbo: processor pending pix/boleto rows in window where
// now - occurred_at exceeds the per-method threshold.
func (r *Repo) FindPendingLimbo(ctx context.Context, f repository.AnomalyFilter, now time.Time, th repository.PendingLimboThresholds) ([]domain.AnomalyDetail, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	pixCutoff := formatTime(now.Add(-th.Pix))
	boletoCutoff := formatTime(now.Add(-th.Boleto))

	q := `SELECT transaction_id, processor, payment_method, amount_cents,
                 currency, status, source, occurred_at
          FROM transactions t
          WHERE t.source = 'processor' AND t.status = 'pending'
            AND (
              (t.payment_method = 'pix'    AND t.occurred_at < ?) OR
              (t.payment_method = 'boleto' AND t.occurred_at < ?)
            )`
	args := []any{pixCutoff, boletoCutoff}
	if where, wArgs := windowClause("t", "occurred_at", f.Window); where != "" {
		q += " AND " + where
		args = append(args, wArgs...)
	}
	q += " ORDER BY t.occurred_at ASC, t.id ASC"
	if lo, lArgs := limitOffsetClause(f.Limit, f.Offset); lo != "" {
		q += lo
		args = append(args, lArgs...)
	}
	rs, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("find pending limbo: %w", err)
	}
	defer rs.Close()

	out := []domain.AnomalyDetail{}
	for rs.Next() {
		var (
			txID, currency, processor                 string
			paymentMethod, status, source, occurredAt string
			amount                                    int64
		)
		if err := rs.Scan(&txID, &processor, &paymentMethod, &amount, &currency, &status, &source, &occurredAt); err != nil {
			return nil, err
		}
		t, err := parseTime(occurredAt)
		if err != nil {
			return nil, err
		}
		var threshold time.Duration
		switch domain.PaymentMethod(paymentMethod) {
		case domain.PaymentMethodPix:
			threshold = th.Pix
		case domain.PaymentMethodBoleto:
			threshold = th.Boleto
		}
		out = append(out, domain.AnomalyDetail{
			TransactionID: txID,
			Processor:     processor,
			PaymentMethod: domain.PaymentMethod(paymentMethod),
			AmountCents:   amount,
			Currency:      currency,
			Status:        domain.Status(status),
			Source:        domain.Source(source),
			OccurredAt:    t,
			Reason:        fmt.Sprintf("pending %s exceeds %s threshold", paymentMethod, threshold),
		})
	}
	return out, rs.Err()
}

func (r *Repo) UniqueTransactionIDsCount(ctx context.Context, w repository.Window) (int, error) {
	if err := w.Validate(); err != nil {
		return 0, err
	}
	q := `SELECT COUNT(DISTINCT transaction_id) FROM transactions t`
	args := []any{}
	if where, wArgs := windowClause("t", "occurred_at", w); where != "" {
		q += " WHERE " + where
		args = append(args, wArgs...)
	}
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("unique tx count: %w", err)
	}
	return n, nil
}

func (r *Repo) totalRowsInWindow(ctx context.Context, w repository.Window) (int, error) {
	q := `SELECT COUNT(*) FROM transactions t`
	args := []any{}
	if where, wArgs := windowClause("t", "occurred_at", w); where != "" {
		q += " WHERE " + where
		args = append(args, wArgs...)
	}
	var n int
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("total rows: %w", err)
	}
	return n, nil
}

func (r *Repo) CountsByWindow(ctx context.Context, w repository.Window, now time.Time, th repository.PendingLimboThresholds) (repository.Counts, error) {
	if err := w.Validate(); err != nil {
		return repository.Counts{}, err
	}
	all := repository.AnomalyFilter{Window: w}

	orph, err := r.FindOrphanedApprovals(ctx, all)
	if err != nil {
		return repository.Counts{}, err
	}
	ghost, err := r.FindGhostOrders(ctx, all)
	if err != nil {
		return repository.Counts{}, err
	}
	dups, err := r.FindDuplicates(ctx, all)
	if err != nil {
		return repository.Counts{}, err
	}
	limbo, err := r.FindPendingLimbo(ctx, all, now, th)
	if err != nil {
		return repository.Counts{}, err
	}
	uniq, err := r.UniqueTransactionIDsCount(ctx, w)
	if err != nil {
		return repository.Counts{}, err
	}
	total, err := r.totalRowsInWindow(ctx, w)
	if err != nil {
		return repository.Counts{}, err
	}

	dupExtra := 0
	for _, g := range dups {
		dupExtra += g.Occurrences - 1
	}

	return repository.Counts{
		Orphaned:        len(orph),
		Ghost:           len(ghost),
		DuplicateGroups: len(dups),
		DuplicateExtra:  dupExtra,
		PendingLimbo:    len(limbo),
		UniqueInWindow:  uniq,
		TotalRowsWindow: total,
	}, nil
}

func (r *Repo) CountsByProcessor(ctx context.Context, w repository.Window, now time.Time, th repository.PendingLimboThresholds) (map[string]repository.Counts, error) {
	return r.countsByKey(ctx, w, now, th, func(d domain.AnomalyDetail) string { return d.Processor })
}

func (r *Repo) CountsByPaymentMethod(ctx context.Context, w repository.Window, now time.Time, th repository.PendingLimboThresholds) (map[string]repository.Counts, error) {
	return r.countsByKey(ctx, w, now, th, func(d domain.AnomalyDetail) string { return string(d.PaymentMethod) })
}

func (r *Repo) countsByKey(ctx context.Context, w repository.Window, now time.Time, th repository.PendingLimboThresholds, key func(domain.AnomalyDetail) string) (map[string]repository.Counts, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	out := map[string]repository.Counts{}
	bump := func(k string, mut func(*repository.Counts)) {
		c := out[k]
		mut(&c)
		out[k] = c
	}
	all := repository.AnomalyFilter{Window: w}

	orph, err := r.FindOrphanedApprovals(ctx, all)
	if err != nil {
		return nil, err
	}
	for _, d := range orph {
		bump(key(d), func(c *repository.Counts) { c.Orphaned++ })
	}
	ghost, err := r.FindGhostOrders(ctx, all)
	if err != nil {
		return nil, err
	}
	for _, d := range ghost {
		bump(key(d), func(c *repository.Counts) { c.Ghost++ })
	}
	dups, err := r.FindDuplicates(ctx, all)
	if err != nil {
		return nil, err
	}
	for _, g := range dups {
		if len(g.Rows) == 0 {
			continue
		}
		k := key(g.Rows[0])
		bump(k, func(c *repository.Counts) {
			c.DuplicateGroups++
			c.DuplicateExtra += g.Occurrences - 1
		})
	}
	limbo, err := r.FindPendingLimbo(ctx, all, now, th)
	if err != nil {
		return nil, err
	}
	for _, d := range limbo {
		bump(key(d), func(c *repository.Counts) { c.PendingLimbo++ })
	}
	return out, nil
}
