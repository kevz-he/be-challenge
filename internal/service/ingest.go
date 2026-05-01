package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/yuno/transaction-health-monitor/internal/domain"
	"github.com/yuno/transaction-health-monitor/internal/repository"
)

// IngestError describes a validation failure for a transaction inside a
// batch. Index is 0-based and references the client's original position.
type IngestError struct {
	Index         int    `json:"index"`
	TransactionID string `json:"transaction_id,omitempty"`
	Error         string `json:"error"`
}

// IngestResult is the response returned by an ingest. In atomic mode, if
// any errors are present, accepted == 0 and nothing is persisted. This is
// documented in ARCHITECTURE.md.
type IngestResult struct {
	Accepted int           `json:"accepted"`
	Errors   []IngestError `json:"errors,omitempty"`
}

// IngestService implements the logic for the POST /v1/transactions and
// /v1/transactions/batch endpoints.
type IngestService struct {
	repo repository.Repository
}

func NewIngestService(repo repository.Repository) *IngestService {
	return &IngestService{repo: repo}
}

// Single ingests a single transaction. It returns a wrapped ErrInvalidInput
// if validation fails.
func (s *IngestService) Single(ctx context.Context, t domain.Transaction) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := s.repo.InsertBatch(ctx, []domain.Transaction{t}); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

// Batch validates all transactions first. If ANY of them is invalid,
// nothing is persisted and the result contains errors[]. If all are valid,
// the entire batch is inserted atomically.
func (s *IngestService) Batch(ctx context.Context, txs []domain.Transaction) (IngestResult, error) {
	if len(txs) == 0 {
		return IngestResult{Accepted: 0}, nil
	}
	var ingestErrors []IngestError
	for i, t := range txs {
		if err := t.Validate(); err != nil {
			ingestErrors = append(ingestErrors, IngestError{
				Index:         i,
				TransactionID: t.TransactionID,
				Error:         unwrapMessage(err),
			})
		}
	}
	if len(ingestErrors) > 0 {
		return IngestResult{Accepted: 0, Errors: ingestErrors}, domain.ErrInvalidInput
	}
	if err := s.repo.InsertBatch(ctx, txs); err != nil {
		return IngestResult{}, fmt.Errorf("insert batch: %w", err)
	}
	return IngestResult{Accepted: len(txs)}, nil
}

func unwrapMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, domain.ErrInvalidInput) {
		return err.Error()
	}
	return err.Error()
}
