package github

import (
	"testing"

	gogithub "github.com/google/go-github/v74/github"
)

func TestFilterFailedOriginalDeliveries(t *testing.T) {
	t.Parallel()
	now := gogithub.Timestamp{}
	deliveries := []*gogithub.HookDelivery{
		{ID: gogithub.Ptr(int64(1)), Action: gogithub.Ptr("queued"), StatusCode: gogithub.Ptr(500), Redelivery: gogithub.Ptr(false), DeliveredAt: &now},
		{ID: gogithub.Ptr(int64(2)), Action: gogithub.Ptr("queued"), StatusCode: gogithub.Ptr(200), Redelivery: gogithub.Ptr(false), DeliveredAt: &now},
		{ID: gogithub.Ptr(int64(3)), Action: gogithub.Ptr("in_progress"), StatusCode: gogithub.Ptr(500), Redelivery: gogithub.Ptr(false), DeliveredAt: &now},
		{ID: gogithub.Ptr(int64(4)), Action: gogithub.Ptr("completed"), StatusCode: gogithub.Ptr(502), Redelivery: gogithub.Ptr(true), DeliveredAt: &now},
		{ID: gogithub.Ptr(int64(5)), Action: nil, StatusCode: gogithub.Ptr(500), Redelivery: gogithub.Ptr(false), DeliveredAt: &now},
		{ID: gogithub.Ptr(int64(6)), Action: gogithub.Ptr("completed"), StatusCode: gogithub.Ptr(404), Redelivery: gogithub.Ptr(false), DeliveredAt: &now},
	}
	got := filterFailedOriginalDeliveries(deliveries)
	if len(got) != 2 {
		t.Fatalf("filterFailedOriginalDeliveries returned %d, want 2", len(got))
	}
	if *got[0].ID != 1 || *got[1].ID != 6 {
		t.Fatalf("got ids %d,%d want 1,6", *got[0].ID, *got[1].ID)
	}
}

func TestHasAnkaTemplateLabel(t *testing.T) {
	t.Parallel()
	if hasAnkaTemplateLabel([]string{"ubuntu-latest"}) {
		t.Fatal("ubuntu-latest should not match")
	}
	if !hasAnkaTemplateLabel([]string{"self-hosted", "anka-template:abc"}) {
		t.Fatal("anka-template label should match")
	}
	if hasAnkaTemplateLabel(nil) {
		t.Fatal("empty labels should not match")
	}
}

func TestShouldSkipQueuedRedelivery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status     string
		conclusion string
		wantSkip   bool
	}{
		{status: "completed", wantSkip: true},
		{status: "in_progress", wantSkip: true},
		{status: "completed", conclusion: "cancelled", wantSkip: true},
		{status: "queued", conclusion: "cancelled", wantSkip: true},
		{status: "queued", wantSkip: false},
		{status: "waiting", wantSkip: false},
	}
	for _, tt := range tests {
		t.Run(tt.status+"_"+tt.conclusion, func(t *testing.T) {
			got := shouldSkipQueuedRedelivery(tt.status, tt.conclusion)
			if got != tt.wantSkip {
				t.Errorf("shouldSkipQueuedRedelivery(%q, %q) = %v, want %v", tt.status, tt.conclusion, got, tt.wantSkip)
			}
		})
	}
}

func TestRedeliveryCallEstimate(t *testing.T) {
	t.Parallel()
	if got := redeliveryCallEstimate("queued"); got != 3 {
		t.Errorf("queued estimate = %d, want 3", got)
	}
	if got := redeliveryCallEstimate("completed"); got != 2 {
		t.Errorf("completed estimate = %d, want 2", got)
	}
	if got := redeliveryCallEstimate("waiting"); got != 2 {
		t.Errorf("waiting estimate = %d, want 2", got)
	}
}

func TestCountCandidatesFittingBudget(t *testing.T) {
	t.Parallel()
	// oldest processed first: walk from the end
	actions := []string{"completed", "queued", "queued"} // estimates 2, 3, 3
	// budget 5: last item (queued=3) fits, next queued=3 does not
	got := countCandidatesFittingBudget(actions, 5)
	if got != 1 {
		t.Errorf("countCandidatesFittingBudget(5) = %d, want 1", got)
	}
	got = countCandidatesFittingBudget(actions, 6)
	if got != 2 {
		t.Errorf("countCandidatesFittingBudget(6) = %d, want 2", got)
	}
	got = countCandidatesFittingBudget(actions, 1)
	if got != 0 {
		t.Errorf("countCandidatesFittingBudget(1) = %d, want 0", got)
	}
}
