package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
)

// Window defines a closed interval [From, To] (inclusive). If From is nil
// there is no lower bound; if To is nil there is no upper bound. From > To is invalid.
type Window struct {
	From *time.Time
	To   *time.Time
}

func (w Window) Validate() error {
	if w.From != nil && w.To != nil && w.From.After(*w.To) {
		return fmt.Errorf("%w: from > to", domain.ErrInvalidWindow)
	}
	return nil
}

// AnomalyFilter is the common filter shared by anomaly queries.
type AnomalyFilter struct {
	Window Window
	Limit  int
	Offset int
}

func (f AnomalyFilter) Validate() error {
	if err := f.Window.Validate(); err != nil {
		return err
	}
	if f.Limit < 0 {
		return fmt.Errorf("%w: limit < 0", domain.ErrInvalidInput)
	}
	if f.Offset < 0 {
		return fmt.Errorf("%w: offset < 0", domain.ErrInvalidInput)
	}
	return nil
}

// DuplicateGroup represents a (transaction_id, source) group with N>1 occurrences.
type DuplicateGroup struct {
	TransactionID string
	Source        domain.Source
	Occurrences   int
	Rows          []domain.AnomalyDetail
}

// CountsByKey is a grouped map used for breakdowns (processor, payment_method, etc).
type CountsByKey struct {
	Key   string
	Count int
}

// Counts holds the raw counts used for health/breakdown.
type Counts struct {
	Orphaned        int
	Ghost           int
	DuplicateGroups int
	DuplicateExtra  int
	PendingLimbo    int
	UniqueInWindow  int
	TotalRowsWindow int
}

// PendingLimboThresholds are the per-method thresholds for pending limbo
// (configurable via Config).
type PendingLimboThresholds struct {
	Pix    time.Duration
	Boleto time.Duration
}

// Repository is the interface that the service consumes. The SQLite impl
// lives in internal/repository/sqlite. An in-memory fake for service unit
// tests lives in this same package (see fake.go).
type Repository interface {
	Migrate(ctx context.Context) error
	Close() error

	InsertBatch(ctx context.Context, txs []domain.Transaction) error

	ListByWindow(ctx context.Context, w Window, limit, offset int) ([]domain.Transaction, error)

	FindOrphanedApprovals(ctx context.Context, f AnomalyFilter) ([]domain.AnomalyDetail, error)
	FindGhostOrders(ctx context.Context, f AnomalyFilter) ([]domain.AnomalyDetail, error)
	FindDuplicates(ctx context.Context, f AnomalyFilter) ([]DuplicateGroup, error)
	FindPendingLimbo(ctx context.Context, f AnomalyFilter, now time.Time, th PendingLimboThresholds) ([]domain.AnomalyDetail, error)

	CountsByWindow(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (Counts, error)
	CountsByProcessor(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (map[string]Counts, error)
	CountsByPaymentMethod(ctx context.Context, w Window, now time.Time, th PendingLimboThresholds) (map[string]Counts, error)

	UniqueTransactionIDsCount(ctx context.Context, w Window) (int, error)
}
