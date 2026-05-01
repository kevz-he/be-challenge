package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

// Alerts ----------------------------------------------------------------

func TestAlerts_TriggerOnLowScore(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	limboTh := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	health := service.NewHealthService(repo, clk, limboTh)
	alerts := service.NewAlertsService(repo, clk, limboTh, service.AlertThresholds{
		OrphanedMax: 1000, GhostMax: 1000, HealthMin: 0.99, // very strict score floor
	}, health)

	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	rep, err := alerts.Evaluate(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Alert {
		t.Fatalf("expected alert with HealthMin=0.99, got %+v", rep)
	}
	hit := false
	for _, r := range rep.Triggered {
		if r.Rule == "health_score_below_min" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("health_score_below_min rule not triggered: %+v", rep.Triggered)
	}
}

func TestAlerts_TriggerOnOrphanedAndGhost(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	limboTh := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	health := service.NewHealthService(repo, clk, limboTh)
	alerts := service.NewAlertsService(repo, clk, limboTh, service.AlertThresholds{
		OrphanedMax: 5, GhostMax: 5, HealthMin: 0.0,
	}, health)
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	rep, err := alerts.Evaluate(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]bool{}
	for _, r := range rep.Triggered {
		rules[r.Rule] = true
	}
	if !rules["orphaned_threshold_exceeded"] || !rules["ghost_threshold_exceeded"] {
		t.Errorf("expected both orphaned+ghost rules, got %+v", rep.Triggered)
	}
}

func TestAlerts_NoTriggerOnEmpty(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	limboTh := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	health := service.NewHealthService(repo, clk, limboTh)
	alerts := service.NewAlertsService(repo, clk, limboTh, service.AlertThresholds{
		OrphanedMax: 50, GhostMax: 100, HealthMin: 0.95,
	}, health)
	rep, err := alerts.Evaluate(context.Background(), repository.Window{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Alert {
		t.Errorf("empty dataset should not alert, got %+v", rep)
	}
}

func TestAlerts_InvalidWindow(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	limboTh := repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour}
	health := service.NewHealthService(repo, clk, limboTh)
	alerts := service.NewAlertsService(repo, clk, limboTh, service.AlertThresholds{}, health)
	from := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if _, err := alerts.Evaluate(context.Background(), repository.Window{From: &from, To: &to}); err == nil {
		t.Errorf("expected error on invalid window")
	}
}

// Anomalies List for each type -----------------------------------------

func TestAnomalies_ListEachType(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}

	res, err := svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyGhost, Window: w, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != exp.AnomalyCounts["ghost"] {
		t.Errorf("ghost: got %d want %d", len(res.Items), exp.AnomalyCounts["ghost"])
	}

	res, err = svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyDuplicate, Window: w, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != exp.AnomalyCounts["duplicate"] {
		t.Errorf("duplicate groups: got %d want %d", len(res.Groups), exp.AnomalyCounts["duplicate"])
	}

	res, err = svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyPendingLimbo, Window: w, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != exp.AnomalyCounts["pending_limbo"] {
		t.Errorf("pending_limbo: got %d want %d", len(res.Items), exp.AnomalyCounts["pending_limbo"])
	}
}

func TestAnomalies_List_NegativeLimitRejected(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	_, err := svc.List(context.Background(), service.AnomalyQuery{Type: domain.AnomalyOrphaned, Limit: -1})
	if err == nil {
		t.Errorf("expected error for negative limit")
	}
}

func TestAnomalies_Summary_BreakdownByPaymentMethod(t *testing.T) {
	repo, exp := makeFakeWith(t)
	clk := clock.NewFake(exp.Now)
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	w := repository.Window{From: &exp.WindowFrom, To: &exp.WindowTo}
	sum, err := svc.Summary(context.Background(), w, "payment_method")
	if err != nil {
		t.Fatal(err)
	}
	if sum.BreakdownBy != "payment_method" {
		t.Errorf("breakdown_by: got %s", sum.BreakdownBy)
	}
	pmLimbo := sum.Breakdown["pix"]["pending_limbo"] + sum.Breakdown["boleto"]["pending_limbo"]
	if pmLimbo != exp.AnomalyCounts["pending_limbo"] {
		t.Errorf("pix+boleto pending_limbo across breakdown: got %d want %d", pmLimbo, exp.AnomalyCounts["pending_limbo"])
	}
}

func TestAnomalies_Summary_InvalidBreakdown(t *testing.T) {
	repo := repository.NewFake()
	clk := clock.NewFake(time.Now())
	svc := service.NewAnomaliesService(repo, clk, repository.PendingLimboThresholds{Pix: 24 * time.Hour, Boleto: 72 * time.Hour})
	if _, err := svc.Summary(context.Background(), repository.Window{}, "banana"); err == nil {
		t.Errorf("expected error for invalid breakdown")
	}
}
