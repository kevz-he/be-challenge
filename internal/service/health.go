package service

import (
	"context"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// HealthReport is the response for GET /v1/health.
type HealthReport struct {
	Score              float64                    `json:"score"`
	Window             WindowDTO                  `json:"window"`
	UniqueTransactions int                        `json:"unique_transactions"`
	TotalRows          int                        `json:"total_rows"`
	AnomalyCounts      map[string]int             `json:"anomaly_counts"`
	DuplicateExtraRows int                        `json:"duplicate_extra_rows"`
	Breakdown          map[string]HealthBreakdown `json:"breakdown,omitempty"`
	BreakdownBy        string                     `json:"breakdown_by,omitempty"`
}

// HealthBreakdown is the sub-report per processor or payment method.
type HealthBreakdown struct {
	AnomalyCounts      map[string]int `json:"anomaly_counts"`
	DuplicateExtraRows int            `json:"duplicate_extra_rows"`
}

// HealthService computes the score using the formula from the plan:
//
//	duplicates_extra = total_duplicate_rows - duplicate_groups
//	anomalies_total  = orphaned + ghost + duplicates_extra + pending_limbo
//	denominator      = COUNT(DISTINCT transaction_id) in the window
//	score            = clamp(1 - anomalies_total/denominator, 0, 1)
//
// denominator == 0 => score = 1.0 ("empty == healthy").
type HealthService struct {
	repo       repository.Repository
	clock      clock.Clock
	thresholds repository.PendingLimboThresholds
}

func NewHealthService(repo repository.Repository, clk clock.Clock, th repository.PendingLimboThresholds) *HealthService {
	return &HealthService{repo: repo, clock: clk, thresholds: th}
}

func (s *HealthService) Compute(ctx context.Context, w repository.Window, breakdown string) (HealthReport, error) {
	if err := w.Validate(); err != nil {
		return HealthReport{}, err
	}
	now := s.clock.Now()
	counts, err := s.repo.CountsByWindow(ctx, w, now, s.thresholds)
	if err != nil {
		return HealthReport{}, err
	}

	score := computeScore(counts)
	report := HealthReport{
		Score:              score,
		Window:             WindowDTO{From: w.From, To: w.To},
		UniqueTransactions: counts.UniqueInWindow,
		TotalRows:          counts.TotalRowsWindow,
		AnomalyCounts: map[string]int{
			"orphaned":      counts.Orphaned,
			"ghost":         counts.Ghost,
			"duplicate":     counts.DuplicateGroups,
			"pending_limbo": counts.PendingLimbo,
		},
		DuplicateExtraRows: counts.DuplicateExtra,
	}

	switch breakdown {
	case "":
	case "processor":
		bd, err := s.repo.CountsByProcessor(ctx, w, now, s.thresholds)
		if err != nil {
			return HealthReport{}, err
		}
		report.Breakdown = toHealthBreakdown(bd)
		report.BreakdownBy = "processor"
	case "payment_method":
		bd, err := s.repo.CountsByPaymentMethod(ctx, w, now, s.thresholds)
		if err != nil {
			return HealthReport{}, err
		}
		report.Breakdown = toHealthBreakdown(bd)
		report.BreakdownBy = "payment_method"
	}
	return report, nil
}

func computeScore(c repository.Counts) float64 {
	if c.UniqueInWindow == 0 {
		return 1.0
	}
	anomalies := c.Orphaned + c.Ghost + c.DuplicateExtra + c.PendingLimbo
	score := 1.0 - float64(anomalies)/float64(c.UniqueInWindow)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

func toHealthBreakdown(in map[string]repository.Counts) map[string]HealthBreakdown {
	out := make(map[string]HealthBreakdown, len(in))
	for k, c := range in {
		out[k] = HealthBreakdown{
			AnomalyCounts: map[string]int{
				"orphaned":      c.Orphaned,
				"ghost":         c.Ghost,
				"duplicate":     c.DuplicateGroups,
				"pending_limbo": c.PendingLimbo,
			},
			DuplicateExtraRows: c.DuplicateExtra,
		}
	}
	return out
}
