package service

import (
	"context"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// AlertThresholds parameterizes what triggers an alert. They come from config.Config.
type AlertThresholds struct {
	OrphanedMax int     // triggers if orphaned > this value
	GhostMax    int     // triggers if ghost > this value
	HealthMin   float64 // triggers if score < this value
}

// AlertRule is the metric evaluated for a single rule.
type AlertRule struct {
	Rule      string  `json:"rule"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Message   string  `json:"message"`
}

// AlertReport is the response for GET /v1/alerts.
type AlertReport struct {
	Alert     bool        `json:"alert"`
	Window    WindowDTO   `json:"window"`
	Triggered []AlertRule `json:"triggered"`
}

// AlertsService evaluates the configured rules against the counts and health.
type AlertsService struct {
	repo          repository.Repository
	clock         clock.Clock
	limboTh       repository.PendingLimboThresholds
	thresholds    AlertThresholds
	healthService *HealthService
}

func NewAlertsService(repo repository.Repository, clk clock.Clock, limboTh repository.PendingLimboThresholds, th AlertThresholds, healthSvc *HealthService) *AlertsService {
	return &AlertsService{
		repo:          repo,
		clock:         clk,
		limboTh:       limboTh,
		thresholds:    th,
		healthService: healthSvc,
	}
}

func (s *AlertsService) Evaluate(ctx context.Context, w repository.Window) (AlertReport, error) {
	if err := w.Validate(); err != nil {
		return AlertReport{}, err
	}
	rep, err := s.healthService.Compute(ctx, w, "")
	if err != nil {
		return AlertReport{}, err
	}

	out := AlertReport{
		Window:    WindowDTO{From: w.From, To: w.To},
		Triggered: []AlertRule{},
	}
	if rep.AnomalyCounts["orphaned"] > s.thresholds.OrphanedMax {
		out.Triggered = append(out.Triggered, AlertRule{
			Rule:      "orphaned_threshold_exceeded",
			Value:     float64(rep.AnomalyCounts["orphaned"]),
			Threshold: float64(s.thresholds.OrphanedMax),
			Message:   "orphaned approvals exceed threshold",
		})
	}
	if rep.AnomalyCounts["ghost"] > s.thresholds.GhostMax {
		out.Triggered = append(out.Triggered, AlertRule{
			Rule:      "ghost_threshold_exceeded",
			Value:     float64(rep.AnomalyCounts["ghost"]),
			Threshold: float64(s.thresholds.GhostMax),
			Message:   "ghost orders exceed threshold",
		})
	}
	if rep.Score < s.thresholds.HealthMin {
		out.Triggered = append(out.Triggered, AlertRule{
			Rule:      "health_score_below_min",
			Value:     rep.Score,
			Threshold: s.thresholds.HealthMin,
			Message:   "health score below minimum",
		})
	}
	out.Alert = len(out.Triggered) > 0
	return out, nil
}
