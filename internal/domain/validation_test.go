package domain

import (
	"errors"
	"testing"
	"time"
)

func validTransaction() Transaction {
	return Transaction{
		TransactionID: "tx_123",
		OccurredAt:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		AmountCents:   1000,
		Currency:      CurrencyBRL,
		PaymentMethod: PaymentMethodPix,
		Processor:     "stone",
		Status:        StatusApproved,
		Source:        SourceProcessor,
	}
}

func TestTransactionValidate_HappyPath(t *testing.T) {
	if err := validTransaction().Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestTransactionValidate_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Transaction)
	}{
		{"empty transaction_id", func(tx *Transaction) { tx.TransactionID = "" }},
		{"whitespace transaction_id", func(tx *Transaction) { tx.TransactionID = "   " }},
		{"zero occurred_at", func(tx *Transaction) { tx.OccurredAt = time.Time{} }},
		{"amount_cents zero", func(tx *Transaction) { tx.AmountCents = 0 }},
		{"amount_cents negative", func(tx *Transaction) { tx.AmountCents = -1 }},
		{"currency not BRL", func(tx *Transaction) { tx.Currency = "USD" }},
		{"payment_method invalid", func(tx *Transaction) { tx.PaymentMethod = PaymentMethod("crypto") }},
		{"status invalid", func(tx *Transaction) { tx.Status = Status("settled") }},
		{"source invalid", func(tx *Transaction) { tx.Source = Source("gateway") }},
		{"processor empty", func(tx *Transaction) { tx.Processor = "" }},
		{"processor whitespace", func(tx *Transaction) { tx.Processor = "   " }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tx := validTransaction()
			tc.mutate(&tx)
			err := tx.Validate()
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("expected error to wrap ErrInvalidInput, got %v", err)
			}
		})
	}
}
