package github

import (
	"testing"
	"time"

	gogithub "github.com/google/go-github/v74/github"
)

func TestRedeliveryBudgetFromRemaining(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		remaining int
		want      int
	}{
		{name: "zero remaining waits", remaining: 0, want: 0},
		{name: "negative remaining waits", remaining: -1, want: 0},
		{name: "one remaining uses one", remaining: 1, want: 1},
		{name: "ten remaining uses half", remaining: 10, want: 5},
		{name: "odd remaining floors", remaining: 15, want: 7},
		{name: "hundred remaining uses half", remaining: 100, want: 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedeliveryBudgetFromRemaining(tt.remaining)
			if got != tt.want {
				t.Errorf("RedeliveryBudgetFromRemaining(%d) = %d, want %d", tt.remaining, got, tt.want)
			}
		})
	}
}

func TestRedeliveryBudgetCanSpendAndRecord(t *testing.T) {
	t.Parallel()
	b := &RedeliveryBudget{}
	b.StartWindow(100, time.Now().Add(time.Hour))
	if b.Budget() != 50 {
		t.Fatalf("Budget() = %d, want 50", b.Budget())
	}
	if !b.CanSpend(50) {
		t.Fatal("expected CanSpend(50) at start of window")
	}
	if b.CanSpend(51) {
		t.Fatal("expected CanSpend(51) to be false (over 50%)")
	}
	b.Record(49)
	if !b.CanSpend(1) {
		t.Fatal("expected one call left in the 50% budget")
	}
	b.Record(1)
	if b.CanSpend(1) {
		t.Fatal("expected budget exhausted after 50 calls")
	}
}

func TestRedeliveryBudgetApplyRateUpdatesReset(t *testing.T) {
	t.Parallel()
	b := &RedeliveryBudget{}
	reset := time.Now().Add(30 * time.Minute)
	b.ApplyRate(gogithub.Rate{
		Remaining: 80,
		Reset:     gogithub.Timestamp{Time: reset},
	})
	if !b.ResetAt().Equal(reset) {
		t.Fatalf("ResetAt() = %v, want %v", b.ResetAt(), reset)
	}
}
