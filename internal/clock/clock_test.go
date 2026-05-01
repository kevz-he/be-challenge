package clock

import (
	"testing"
	"time"
)

func TestRealNow(t *testing.T) {
	c := New()
	before := time.Now().UTC()
	got := c.Now()
	after := time.Now().UTC()

	if got.IsZero() {
		t.Fatal("Real.Now() returned zero time")
	}
	if got.Location() != time.UTC {
		t.Errorf("Real.Now() location = %v, want UTC", got.Location())
	}
	if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
		t.Errorf("Real.Now() = %v out of expected window [%v, %v]", got, before, after)
	}
}

func TestFakeNow(t *testing.T) {
	base := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	f := NewFake(base)
	if !f.Now().Equal(base) {
		t.Errorf("Fake.Now() = %v, want %v", f.Now(), base)
	}
}

func TestFakeNewFakeNormalizesToUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	local := time.Date(2025, 5, 1, 12, 0, 0, 0, loc)
	f := NewFake(local)
	if f.Now().Location() != time.UTC {
		t.Errorf("location = %v, want UTC", f.Now().Location())
	}
	if !f.Now().Equal(local) {
		t.Errorf("Fake.Now() = %v, want equivalent to %v", f.Now(), local)
	}
}

func TestFakeSet(t *testing.T) {
	f := NewFake(time.Unix(0, 0).UTC())
	target := time.Date(2030, 6, 15, 10, 0, 0, 0, time.UTC)
	f.Set(target)
	if !f.Now().Equal(target) {
		t.Errorf("after Set: Fake.Now() = %v, want %v", f.Now(), target)
	}
}

func TestFakeSetNormalizesToUTC(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	f := NewFake(time.Unix(0, 0).UTC())
	local := time.Date(2025, 5, 1, 9, 0, 0, 0, loc)
	f.Set(local)
	if f.Now().Location() != time.UTC {
		t.Errorf("location = %v, want UTC", f.Now().Location())
	}
	if !f.Now().Equal(local) {
		t.Errorf("Fake.Now() = %v, want equivalent to %v", f.Now(), local)
	}
}

func TestFakeAdvance(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	f := NewFake(base)
	f.Advance(2 * time.Hour)
	want := base.Add(2 * time.Hour)
	if !f.Now().Equal(want) {
		t.Errorf("after Advance: Fake.Now() = %v, want %v", f.Now(), want)
	}
	f.Advance(-30 * time.Minute)
	want = want.Add(-30 * time.Minute)
	if !f.Now().Equal(want) {
		t.Errorf("after negative Advance: Fake.Now() = %v, want %v", f.Now(), want)
	}
}

func TestFakeImplementsClock(t *testing.T) {
	var _ Clock = NewFake(time.Now())
	var _ Clock = New()
}
