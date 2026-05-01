package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/domain"
)

const (
	BaseWindowStart = "2026-04-15T00:00:00Z"
	BaseWindowEnd   = "2026-04-15T06:00:00Z"
	BaseNow         = "2026-04-22T00:00:00Z"

	NumOrphaned     = 20
	NumGhostNoProc  = 8
	NumGhostBadProc = 7
	NumDupGroupsLg  = 4
	NumDupGroupsSm  = 4
	NumPendingLimbo = 10

	PixLimboCount    = 5
	BoletoLimboCount = 5

	AnomalyRows = NumOrphaned +
		NumGhostNoProc +
		(NumGhostBadProc * 2) +
		(NumDupGroupsLg * 4) +
		(NumDupGroupsSm * 3) +
		NumPendingLimbo
)

// Processors are the four canonical processors used by the seed.
// Names are kept generic (ProcessorA..D) to mirror the challenge brief
// rather than leaking specific real-world brand names.
var Processors = []string{"ProcessorA", "ProcessorB", "ProcessorC", "ProcessorD"}

// healthyPairOutcome describes one of the joint (processor, merchant)
// status combinations used for the HEALTHY-* pairs. The weights below
// approximate the realism targets called out in the brief — processor
// ~85/12/3 and merchant ~87/10/3 (approved/declined/pending) — without
// ever crossing into a status combination that would create a new
// anomaly for HEALTHY-* IDs.
//
// Why this matters: turning a healthy pair into "processor declined +
// merchant approved" or "processor pending pix/boleto with old enough
// occurred_at" would silently inflate ghost / pending_limbo counts and
// drift away from the oracle. Every entry below has been hand-checked
// against §6 and §8 of CLAUDE.md to guarantee no anomaly is produced.
type healthyPairOutcome struct {
	weight      int
	proc, merch domain.Status
	// forceCC is true when the joint status combination is only safe
	// for credit_card payment methods (e.g. processor pending must be
	// CC, otherwise PIX/Boleto would trip pending_limbo with the seed's
	// 7-day-old `now`).
	forceCC bool
}

var healthyPairOutcomes = []healthyPairOutcome{
	{weight: 84, proc: domain.StatusApproved, merch: domain.StatusApproved},
	{weight: 1, proc: domain.StatusApproved, merch: domain.StatusDeclined},
	{weight: 9, proc: domain.StatusDeclined, merch: domain.StatusDeclined},
	{weight: 3, proc: domain.StatusDeclined, merch: domain.StatusPending},
	{weight: 3, proc: domain.StatusPending, merch: domain.StatusApproved, forceCC: true},
}

func pickHealthyOutcome(rng *rand.Rand) healthyPairOutcome {
	total := 0
	for _, o := range healthyPairOutcomes {
		total += o.weight
	}
	r := rng.Intn(total)
	acc := 0
	for _, o := range healthyPairOutcomes {
		acc += o.weight
		if r < acc {
			return o
		}
	}
	return healthyPairOutcomes[0]
}

// AnomalyIDs holds the deterministic transaction_id sets per anomaly type
// produced by the seed. Tests use these to assert set equality, not just
// counts: it's the difference between "we found 20 orphans" and "we found
// EXACTLY these 20 orphans and no others".
type AnomalyIDs struct {
	Orphaned     []string `json:"orphaned"`
	Ghost        []string `json:"ghost"`
	Duplicate    []string `json:"duplicate"`
	PendingLimbo []string `json:"pending_limbo"`
}

type ExpectedCounts struct {
	WindowFrom           time.Time      `json:"window_from"`
	WindowTo             time.Time      `json:"window_to"`
	Now                  time.Time      `json:"now"`
	TotalRows            int            `json:"total_rows"`
	UniqueTransactionIDs int            `json:"unique_transaction_ids_in_window"`
	AnomalyCounts        map[string]int `json:"anomaly_counts"`
	DuplicateExtraRows   int            `json:"duplicate_extra_rows"`
	HealthyPairs         int            `json:"healthy_pairs"`
	HealthScore          float64        `json:"health_score"`
	// ExpectedHealthScore mirrors HealthScore. The duplicated key matches the
	// extended-oracle format documented in docs/ARCHITECTURE.md so reviewers
	// using verify.sh can grep either name.
	ExpectedHealthScore float64    `json:"expected_health_score"`
	IDs                 AnomalyIDs `json:"ids"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "seed: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seedFlag := fs.Int64("seed", 42, "deterministic seed")
	totalFlag := fs.Int("total", 500, "minimum total transactions (>=500)")
	outFlag := fs.String("out", "testdata/transactions.json", "output path for transactions JSON array")
	expectedFlag := fs.String("expected", "testdata/expected_counts.json", "output path for expected counts oracle")
	ingestURL := fs.String("ingest-url", "", "if set, POST {\"transactions\":[...]} to this URL")
	sentinel := fs.String("sentinel", "", "if set, skip if file exists; create it after a successful ingest")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sentinel != "" {
		if _, err := os.Stat(*sentinel); err == nil {
			fmt.Fprintf(stdout, "sentinel %s exists, skipping seed\n", *sentinel)
			return nil
		}
	}

	if *totalFlag < 500 {
		fmt.Fprintf(stderr, "seed: --total %d below minimum, using 500\n", *totalFlag)
		*totalFlag = 500
	}

	windowStart, err := time.Parse(time.RFC3339, BaseWindowStart)
	if err != nil {
		return err
	}
	windowEnd, err := time.Parse(time.RFC3339, BaseWindowEnd)
	if err != nil {
		return err
	}
	now, err := time.Parse(time.RFC3339, BaseNow)
	if err != nil {
		return err
	}

	rng := rand.New(rand.NewSource(*seedFlag))
	healthyPairs := (*totalFlag - AnomalyRows + 1) / 2
	if healthyPairs < 1 {
		healthyPairs = 1
	}

	txs := generate(rng, healthyPairs, windowStart, windowEnd)
	expected := buildExpected(txs, windowStart, windowEnd, now, healthyPairs)

	if err := writeJSONFile(*outFlag, txs); err != nil {
		return fmt.Errorf("write transactions: %w", err)
	}
	if err := writeJSONFile(*expectedFlag, expected); err != nil {
		return fmt.Errorf("write expected: %w", err)
	}

	fmt.Fprintf(stdout, "wrote %d transactions to %s\n", len(txs), *outFlag)
	fmt.Fprintf(stdout, "wrote expected counts to %s\n", *expectedFlag)
	fmt.Fprintf(stdout, "summary: orphaned=%d ghost=%d duplicate_groups=%d duplicate_extra=%d pending_limbo=%d unique_in_window=%d health_score=%.4f\n",
		expected.AnomalyCounts["orphaned"],
		expected.AnomalyCounts["ghost"],
		expected.AnomalyCounts["duplicate"],
		expected.DuplicateExtraRows,
		expected.AnomalyCounts["pending_limbo"],
		expected.UniqueTransactionIDs,
		expected.HealthScore,
	)

	if *ingestURL != "" {
		if err := postBatch(*ingestURL, txs); err != nil {
			return fmt.Errorf("ingest: %w", err)
		}
		fmt.Fprintf(stdout, "ingested %d transactions to %s\n", len(txs), *ingestURL)
	}

	if *sentinel != "" {
		if err := writeSentinel(*sentinel); err != nil {
			return fmt.Errorf("write sentinel: %w", err)
		}
	}
	return nil
}

func writeSentinel(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
}

func generate(rng *rand.Rand, healthyPairs int, windowStart, windowEnd time.Time) []domain.Transaction {
	txs := make([]domain.Transaction, 0, healthyPairs*2+AnomalyRows)

	for i := 0; i < healthyPairs; i++ {
		txID := fmt.Sprintf("HEALTHY-%05d", i)
		ts := randTimeIn(rng, windowStart, windowEnd)
		amount := randAmount(rng)
		outcome := pickHealthyOutcome(rng)
		method := randMethod(rng)
		if outcome.forceCC {
			method = domain.PaymentMethodCreditCard
		}
		proc := Processors[rng.Intn(len(Processors))]

		txs = append(txs, domain.Transaction{
			TransactionID: txID,
			OccurredAt:    ts,
			AmountCents:   amount,
			Currency:      domain.CurrencyBRL,
			PaymentMethod: method,
			Processor:     proc,
			Status:        outcome.proc,
			Source:        domain.SourceProcessor,
		})

		merchantTS := ts.Add(time.Duration(rng.Intn(120)+1) * time.Second)
		if merchantTS.After(windowEnd) {
			merchantTS = windowEnd
		}
		txs = append(txs, domain.Transaction{
			TransactionID: txID,
			OccurredAt:    merchantTS,
			AmountCents:   amount,
			Currency:      domain.CurrencyBRL,
			PaymentMethod: method,
			Processor:     proc,
			Status:        outcome.merch,
			Source:        domain.SourceMerchantOrderSystem,
		})
	}

	for i := 0; i < NumOrphaned; i++ {
		txs = append(txs, domain.Transaction{
			TransactionID: fmt.Sprintf("ORPHAN-%03d", i),
			OccurredAt:    randTimeIn(rng, windowStart, windowEnd),
			AmountCents:   randAmount(rng),
			Currency:      domain.CurrencyBRL,
			PaymentMethod: randMethod(rng),
			Processor:     Processors[rng.Intn(len(Processors))],
			Status:        domain.StatusApproved,
			Source:        domain.SourceProcessor,
		})
	}

	for i := 0; i < NumGhostNoProc; i++ {
		txs = append(txs, domain.Transaction{
			TransactionID: fmt.Sprintf("GHOST-NOPROC-%03d", i),
			OccurredAt:    randTimeIn(rng, windowStart, windowEnd),
			AmountCents:   randAmount(rng),
			Currency:      domain.CurrencyBRL,
			PaymentMethod: randMethod(rng),
			Processor:     Processors[rng.Intn(len(Processors))],
			Status:        domain.StatusApproved,
			Source:        domain.SourceMerchantOrderSystem,
		})
	}

	for i := 0; i < NumGhostBadProc; i++ {
		txID := fmt.Sprintf("GHOST-BADPROC-%03d", i)
		ts := randTimeIn(rng, windowStart, windowEnd)
		method := randMethod(rng)
		proc := Processors[rng.Intn(len(Processors))]
		amount := randAmount(rng)
		st := domain.StatusDeclined
		if i%2 == 0 {
			st = domain.StatusFailed
		}
		procTS := ts.Add(-30 * time.Second)
		if procTS.Before(windowStart) {
			procTS = windowStart
		}
		txs = append(txs,
			domain.Transaction{
				TransactionID: txID, OccurredAt: procTS, AmountCents: amount,
				Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
				Status: st, Source: domain.SourceProcessor,
			},
			domain.Transaction{
				TransactionID: txID, OccurredAt: ts, AmountCents: amount,
				Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
				Status: domain.StatusApproved, Source: domain.SourceMerchantOrderSystem,
			},
		)
	}

	for i := 0; i < NumDupGroupsLg; i++ {
		txID := fmt.Sprintf("DUP-PROC-%03d", i)
		ts := randTimeIn(rng, windowStart, windowEnd)
		method := randMethod(rng)
		proc := Processors[rng.Intn(len(Processors))]
		amount := randAmount(rng)
		for j := 0; j < 3; j++ {
			rowTS := ts.Add(time.Duration(j) * time.Second)
			if rowTS.After(windowEnd) {
				rowTS = windowEnd
			}
			txs = append(txs, domain.Transaction{
				TransactionID: txID, OccurredAt: rowTS, AmountCents: amount,
				Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
				Status: domain.StatusApproved, Source: domain.SourceProcessor,
			})
		}
		mts := ts.Add(10 * time.Second)
		if mts.After(windowEnd) {
			mts = windowEnd
		}
		txs = append(txs, domain.Transaction{
			TransactionID: txID, OccurredAt: mts, AmountCents: amount,
			Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
			Status: domain.StatusApproved, Source: domain.SourceMerchantOrderSystem,
		})
	}

	for i := 0; i < NumDupGroupsSm; i++ {
		txID := fmt.Sprintf("DUP-MERCH-%03d", i)
		ts := randTimeIn(rng, windowStart, windowEnd)
		method := randMethod(rng)
		proc := Processors[rng.Intn(len(Processors))]
		amount := randAmount(rng)
		txs = append(txs, domain.Transaction{
			TransactionID: txID, OccurredAt: ts, AmountCents: amount,
			Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
			Status: domain.StatusApproved, Source: domain.SourceProcessor,
		})
		for j := 0; j < 2; j++ {
			rowTS := ts.Add(time.Duration(j+1) * time.Second)
			if rowTS.After(windowEnd) {
				rowTS = windowEnd
			}
			txs = append(txs, domain.Transaction{
				TransactionID: txID, OccurredAt: rowTS, AmountCents: amount,
				Currency: domain.CurrencyBRL, PaymentMethod: method, Processor: proc,
				Status: domain.StatusApproved, Source: domain.SourceMerchantOrderSystem,
			})
		}
	}

	for i := 0; i < NumPendingLimbo; i++ {
		method := domain.PaymentMethodPix
		if i >= PixLimboCount {
			method = domain.PaymentMethodBoleto
		}
		txs = append(txs, domain.Transaction{
			TransactionID: fmt.Sprintf("LIMBO-%03d", i),
			OccurredAt:    randTimeIn(rng, windowStart, windowEnd),
			AmountCents:   randAmount(rng),
			Currency:      domain.CurrencyBRL,
			PaymentMethod: method,
			Processor:     Processors[rng.Intn(len(Processors))],
			Status:        domain.StatusPending,
			Source:        domain.SourceProcessor,
		})
	}

	rng.Shuffle(len(txs), func(i, j int) { txs[i], txs[j] = txs[j], txs[i] })
	return txs
}

func randTimeIn(rng *rand.Rand, from, to time.Time) time.Time {
	delta := to.Sub(from)
	offset := time.Duration(rng.Int63n(int64(delta)))
	return from.Add(offset).UTC()
}

func randAmount(rng *rand.Rand) int64 {
	return int64(500 + rng.Intn(50_000))
}

func randMethod(rng *rand.Rand) domain.PaymentMethod {
	r := rng.Intn(100)
	switch {
	case r < 60:
		return domain.PaymentMethodCreditCard
	case r < 90:
		return domain.PaymentMethodPix
	default:
		return domain.PaymentMethodBoleto
	}
}

func buildExpected(txs []domain.Transaction, windowFrom, windowTo, now time.Time, healthyPairs int) ExpectedCounts {
	uniq := map[string]struct{}{}
	for _, t := range txs {
		if !t.OccurredAt.Before(windowFrom) && !t.OccurredAt.After(windowTo) {
			uniq[t.TransactionID] = struct{}{}
		}
	}

	dupExtra := (NumDupGroupsLg * (3 - 1)) + (NumDupGroupsSm * (2 - 1))
	anomaliesTotal := NumOrphaned + (NumGhostNoProc + NumGhostBadProc) + dupExtra + NumPendingLimbo

	denom := len(uniq)
	score := 1.0
	if denom > 0 {
		score = 1.0 - float64(anomaliesTotal)/float64(denom)
	}
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	ids := buildExpectedIDs()

	return ExpectedCounts{
		WindowFrom:           windowFrom,
		WindowTo:             windowTo,
		Now:                  now,
		TotalRows:            len(txs),
		UniqueTransactionIDs: denom,
		AnomalyCounts: map[string]int{
			"orphaned":      NumOrphaned,
			"ghost":         NumGhostNoProc + NumGhostBadProc,
			"duplicate":     NumDupGroupsLg + NumDupGroupsSm,
			"pending_limbo": NumPendingLimbo,
		},
		DuplicateExtraRows:  dupExtra,
		HealthyPairs:        healthyPairs,
		HealthScore:         score,
		ExpectedHealthScore: score,
		IDs:                 ids,
	}
}

// buildExpectedIDs returns the deterministic transaction_id sets per anomaly
// type produced by the generator. Keep this in sync with generate(): the
// constants and ID prefixes are the contract that lets the oracle assert
// "exactly these IDs and no others".
func buildExpectedIDs() AnomalyIDs {
	out := AnomalyIDs{
		Orphaned:     make([]string, 0, NumOrphaned),
		Ghost:        make([]string, 0, NumGhostNoProc+NumGhostBadProc),
		Duplicate:    make([]string, 0, NumDupGroupsLg+NumDupGroupsSm),
		PendingLimbo: make([]string, 0, NumPendingLimbo),
	}
	for i := 0; i < NumOrphaned; i++ {
		out.Orphaned = append(out.Orphaned, fmt.Sprintf("ORPHAN-%03d", i))
	}
	for i := 0; i < NumGhostNoProc; i++ {
		out.Ghost = append(out.Ghost, fmt.Sprintf("GHOST-NOPROC-%03d", i))
	}
	for i := 0; i < NumGhostBadProc; i++ {
		out.Ghost = append(out.Ghost, fmt.Sprintf("GHOST-BADPROC-%03d", i))
	}
	for i := 0; i < NumDupGroupsLg; i++ {
		out.Duplicate = append(out.Duplicate, fmt.Sprintf("DUP-PROC-%03d", i))
	}
	for i := 0; i < NumDupGroupsSm; i++ {
		out.Duplicate = append(out.Duplicate, fmt.Sprintf("DUP-MERCH-%03d", i))
	}
	for i := 0; i < NumPendingLimbo; i++ {
		out.PendingLimbo = append(out.PendingLimbo, fmt.Sprintf("LIMBO-%03d", i))
	}
	sort.Strings(out.Orphaned)
	sort.Strings(out.Ghost)
	sort.Strings(out.Duplicate)
	sort.Strings(out.PendingLimbo)
	return out
}

func writeJSONFile(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func postBatch(url string, txs []domain.Transaction) error {
	body := map[string][]domain.Transaction{"transactions": txs}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ingest http %d: %s", resp.StatusCode, string(body))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// sortTransactionsForOracle is kept for potential reproducibility tooling.
func sortTransactionsForOracle(txs []domain.Transaction) {
	sort.SliceStable(txs, func(i, j int) bool {
		if txs[i].OccurredAt.Equal(txs[j].OccurredAt) {
			return txs[i].TransactionID < txs[j].TransactionID
		}
		return txs[i].OccurredAt.Before(txs[j].OccurredAt)
	})
}
