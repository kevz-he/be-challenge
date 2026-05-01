package domain

import (
	"fmt"
	"strings"
)

func (t Transaction) Validate() error {
	if strings.TrimSpace(t.TransactionID) == "" {
		return fmt.Errorf("%w: transaction_id: must not be empty", ErrInvalidInput)
	}
	if t.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at: must not be zero", ErrInvalidInput)
	}
	if t.AmountCents <= 0 {
		return fmt.Errorf("%w: amount_cents: must be greater than zero", ErrInvalidInput)
	}
	if t.Currency != CurrencyBRL {
		return fmt.Errorf("%w: currency: must be %q", ErrInvalidInput, CurrencyBRL)
	}
	if !t.PaymentMethod.Valid() {
		return fmt.Errorf("%w: payment_method: unknown value %q", ErrInvalidInput, string(t.PaymentMethod))
	}
	if !t.Status.Valid() {
		return fmt.Errorf("%w: status: unknown value %q", ErrInvalidInput, string(t.Status))
	}
	if !t.Source.Valid() {
		return fmt.Errorf("%w: source: unknown value %q", ErrInvalidInput, string(t.Source))
	}
	if strings.TrimSpace(t.Processor) == "" {
		return fmt.Errorf("%w: processor: must not be empty", ErrInvalidInput)
	}
	return nil
}
