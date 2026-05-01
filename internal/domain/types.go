package domain

import "time"

const CurrencyBRL = "BRL"

type PaymentMethod string

const (
	PaymentMethodCreditCard PaymentMethod = "credit_card"
	PaymentMethodPix        PaymentMethod = "pix"
	PaymentMethodBoleto     PaymentMethod = "boleto"
)

func (p PaymentMethod) Valid() bool {
	switch p {
	case PaymentMethodCreditCard, PaymentMethodPix, PaymentMethodBoleto:
		return true
	}
	return false
}

type Status string

const (
	StatusApproved    Status = "approved"
	StatusDeclined    Status = "declined"
	StatusPending     Status = "pending"
	StatusFailed      Status = "failed"
	StatusRefunded    Status = "refunded"
	StatusChargedback Status = "chargedback"
)

func (s Status) Valid() bool {
	switch s {
	case StatusApproved, StatusDeclined, StatusPending, StatusFailed, StatusRefunded, StatusChargedback:
		return true
	}
	return false
}

type Source string

const (
	SourceProcessor           Source = "processor"
	SourceMerchantOrderSystem Source = "merchant_order_system"
)

func (s Source) Valid() bool {
	switch s {
	case SourceProcessor, SourceMerchantOrderSystem:
		return true
	}
	return false
}

type Transaction struct {
	TransactionID string        `json:"transaction_id"`
	OccurredAt    time.Time     `json:"occurred_at"`
	AmountCents   int64         `json:"amount_cents"`
	Currency      string        `json:"currency"`
	PaymentMethod PaymentMethod `json:"payment_method"`
	Processor     string        `json:"processor"`
	Status        Status        `json:"status"`
	Source        Source        `json:"source"`
}

type AnomalyType string

const (
	AnomalyOrphaned     AnomalyType = "orphaned"
	AnomalyGhost        AnomalyType = "ghost"
	AnomalyDuplicate    AnomalyType = "duplicate"
	AnomalyPendingLimbo AnomalyType = "pending_limbo"
)

func (a AnomalyType) Valid() bool {
	switch a {
	case AnomalyOrphaned, AnomalyGhost, AnomalyDuplicate, AnomalyPendingLimbo:
		return true
	}
	return false
}

type AnomalyDetail struct {
	TransactionID string        `json:"transaction_id"`
	Processor     string        `json:"processor"`
	PaymentMethod PaymentMethod `json:"payment_method"`
	AmountCents   int64         `json:"amount_cents"`
	Currency      string        `json:"currency"`
	Status        Status        `json:"status"`
	Source        Source        `json:"source"`
	OccurredAt    time.Time     `json:"occurred_at"`
	Reason        string        `json:"reason"`
}

type Anomaly struct {
	Type   AnomalyType   `json:"type"`
	Detail AnomalyDetail `json:"detail"`
}
