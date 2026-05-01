package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// AnomalyQuery is the input for listing anomalies. If Type is "" we return
// the summary with sample[5] per type.
type AnomalyQuery struct {
	Type      domain.AnomalyType
	Window    repository.Window
	Limit     int
	Offset    int
	Breakdown string // "" | "processor" | "payment_method"
}

// AnomalyListResult returns the paginated detail for a specific type.
type AnomalyListResult struct {
	Type   domain.AnomalyType          `json:"type"`
	Items  []domain.AnomalyDetail      `json:"items"`
	Groups []repository.DuplicateGroup `json:"groups,omitempty"`
	Window WindowDTO                   `json:"window"`
	Paging PagingDTO                   `json:"paging"`
}

// AnomalySummary is the response when no type is specified.
type AnomalySummary struct {
	Counts             map[string]int                    `json:"counts"`
	DuplicateExtraRows int                               `json:"duplicate_extra_rows"`
	Samples            map[string][]domain.AnomalyDetail `json:"samples"`
	Window             WindowDTO                         `json:"window"`
}

type WindowDTO struct {
	From *time.Time `json:"from,omitempty"`
	To   *time.Time `json:"to,omitempty"`
}

type PagingDTO struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// AnomaliesService handles anomaly queries.
type AnomaliesService struct {
	repo       repository.Repository
	clock      clock.Clock
	thresholds repository.PendingLimboThresholds
}

func NewAnomaliesService(repo repository.Repository, clk clock.Clock, th repository.PendingLimboThresholds) *AnomaliesService {
	return &AnomaliesService{repo: repo, clock: clk, thresholds: th}
}

const defaultSample = 5

// List returns the paginated detail for a specific anomaly type.
func (s *AnomaliesService) List(ctx context.Context, q AnomalyQuery) (AnomalyListResult, error) {
	if !q.Type.Valid() {
		return AnomalyListResult{}, fmt.Errorf("%w: invalid anomaly type", domain.ErrInvalidInput)
	}
	if err := q.Window.Validate(); err != nil {
		return AnomalyListResult{}, err
	}
	if q.Limit < 0 {
		return AnomalyListResult{}, fmt.Errorf("%w: limit < 0", domain.ErrInvalidInput)
	}
	if q.Offset < 0 {
		return AnomalyListResult{}, fmt.Errorf("%w: offset < 0", domain.ErrInvalidInput)
	}

	filter := repository.AnomalyFilter{Window: q.Window, Limit: q.Limit, Offset: q.Offset}
	res := AnomalyListResult{
		Type:   q.Type,
		Window: WindowDTO{From: q.Window.From, To: q.Window.To},
		Paging: PagingDTO{Limit: q.Limit, Offset: q.Offset},
	}

	switch q.Type {
	case domain.AnomalyOrphaned:
		items, err := s.repo.FindOrphanedApprovals(ctx, filter)
		if err != nil {
			return res, err
		}
		res.Items = items
	case domain.AnomalyGhost:
		items, err := s.repo.FindGhostOrders(ctx, filter)
		if err != nil {
			return res, err
		}
		res.Items = items
	case domain.AnomalyDuplicate:
		groups, err := s.repo.FindDuplicates(ctx, filter)
		if err != nil {
			return res, err
		}
		res.Groups = groups
	case domain.AnomalyPendingLimbo:
		items, err := s.repo.FindPendingLimbo(ctx, filter, s.clock.Now(), s.thresholds)
		if err != nil {
			return res, err
		}
		res.Items = items
	}
	if res.Items == nil {
		res.Items = []domain.AnomalyDetail{}
	}
	return res, nil
}

// Summary returns the counts and a sample (5) per type. Useful for the
// /v1/anomalies endpoint without ?type.
func (s *AnomaliesService) Summary(ctx context.Context, w repository.Window) (AnomalySummary, error) {
	if err := w.Validate(); err != nil {
		return AnomalySummary{}, err
	}
	now := s.clock.Now()
	counts, err := s.repo.CountsByWindow(ctx, w, now, s.thresholds)
	if err != nil {
		return AnomalySummary{}, err
	}

	sampleFilter := repository.AnomalyFilter{Window: w, Limit: defaultSample, Offset: 0}
	orph, err := s.repo.FindOrphanedApprovals(ctx, sampleFilter)
	if err != nil {
		return AnomalySummary{}, err
	}
	ghost, err := s.repo.FindGhostOrders(ctx, sampleFilter)
	if err != nil {
		return AnomalySummary{}, err
	}
	dups, err := s.repo.FindDuplicates(ctx, sampleFilter)
	if err != nil {
		return AnomalySummary{}, err
	}
	limbo, err := s.repo.FindPendingLimbo(ctx, sampleFilter, now, s.thresholds)
	if err != nil {
		return AnomalySummary{}, err
	}

	dupSample := []domain.AnomalyDetail{}
	for _, g := range dups {
		if len(g.Rows) > 0 {
			dupSample = append(dupSample, g.Rows[0])
		}
		if len(dupSample) >= defaultSample {
			break
		}
	}

	return AnomalySummary{
		Counts: map[string]int{
			"orphaned":      counts.Orphaned,
			"ghost":         counts.Ghost,
			"duplicate":     counts.DuplicateGroups,
			"pending_limbo": counts.PendingLimbo,
		},
		DuplicateExtraRows: counts.DuplicateExtra,
		Samples: map[string][]domain.AnomalyDetail{
			"orphaned":      ensureSlice(orph),
			"ghost":         ensureSlice(ghost),
			"duplicate":     ensureSlice(dupSample),
			"pending_limbo": ensureSlice(limbo),
		},
		Window: WindowDTO{From: w.From, To: w.To},
	}, nil
}

func ensureSlice(s []domain.AnomalyDetail) []domain.AnomalyDetail {
	if s == nil {
		return []domain.AnomalyDetail{}
	}
	return s
}

// IsValidationError is a helper for handlers to translate errors.
func IsValidationError(err error) bool {
	return errors.Is(err, domain.ErrInvalidInput) || errors.Is(err, domain.ErrInvalidWindow)
}
