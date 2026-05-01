package domain

import "testing"

func TestPaymentMethodValid(t *testing.T) {
	tests := []struct {
		name string
		pm   PaymentMethod
		want bool
	}{
		{"credit_card", PaymentMethodCreditCard, true},
		{"pix", PaymentMethodPix, true},
		{"boleto", PaymentMethodBoleto, true},
		{"empty", PaymentMethod(""), false},
		{"unknown", PaymentMethod("crypto"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pm.Valid(); got != tc.want {
				t.Errorf("PaymentMethod(%q).Valid() = %v, want %v", tc.pm, got, tc.want)
			}
		})
	}
}

func TestStatusValid(t *testing.T) {
	tests := []struct {
		name string
		s    Status
		want bool
	}{
		{"approved", StatusApproved, true},
		{"declined", StatusDeclined, true},
		{"pending", StatusPending, true},
		{"failed", StatusFailed, true},
		{"refunded", StatusRefunded, true},
		{"chargedback", StatusChargedback, true},
		{"empty", Status(""), false},
		{"unknown", Status("settled"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Valid(); got != tc.want {
				t.Errorf("Status(%q).Valid() = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestSourceValid(t *testing.T) {
	tests := []struct {
		name string
		s    Source
		want bool
	}{
		{"processor", SourceProcessor, true},
		{"merchant_order_system", SourceMerchantOrderSystem, true},
		{"empty", Source(""), false},
		{"unknown", Source("gateway"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Valid(); got != tc.want {
				t.Errorf("Source(%q).Valid() = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestAnomalyTypeValid(t *testing.T) {
	tests := []struct {
		name string
		a    AnomalyType
		want bool
	}{
		{"orphaned", AnomalyOrphaned, true},
		{"ghost", AnomalyGhost, true},
		{"duplicate", AnomalyDuplicate, true},
		{"pending_limbo", AnomalyPendingLimbo, true},
		{"empty", AnomalyType(""), false},
		{"unknown", AnomalyType("weird"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Valid(); got != tc.want {
				t.Errorf("AnomalyType(%q).Valid() = %v, want %v", tc.a, got, tc.want)
			}
		})
	}
}
