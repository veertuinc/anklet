package jobqueue

import (
	"encoding/json"
	"testing"

	"github.com/veertuinc/anklet/internal/anka"
)

// fixtureJob mimics the shape both internal/github.QueueJob and
// internal/azure_devops.QueueJob will take after Phase 0: a service-specific
// outer struct embedding BaseQueueJob.
type fixtureJob struct {
	BaseQueueJob
	ServiceField string `json:"service_field"`
}

func TestBaseQueueJob_JSONRoundTrip(t *testing.T) {
	original := fixtureJob{
		BaseQueueJob: BaseQueueJob{
			Type:     "FixturePayload",
			Action:   ActionQueued,
			Attempts: 2,
			PausedOn: "host-7",
			AnkaVM:   anka.VM{Name: "vm-a", CPUCount: 4, MEMBytes: 8 << 30},
		},
		ServiceField: "hello",
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped fixtureJob
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if roundTripped != original {
		t.Errorf("round-trip mismatch:\n got = %+v\nwant = %+v", roundTripped, original)
	}
}

func TestBaseQueueJob_JSONFieldNames(t *testing.T) {
	job := fixtureJob{
		BaseQueueJob: BaseQueueJob{
			Type:     "T",
			Action:   ActionInProgress,
			Attempts: 1,
			PausedOn: "",
			AnkaVM:   anka.VM{Name: "vm"},
		},
		ServiceField: "s",
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Must round-trip into a plain map so we can assert the wire-format keys
	// match what is already stored in Redis.
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}
	for _, key := range []string{"type", "action", "attempts", "paused_on", "anka_vm", "service_field"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("expected JSON key %q to be present in: %v", key, asMap)
		}
	}
}

func TestStatusAndActionConstants(t *testing.T) {
	pairs := []struct {
		name string
		got  string
		want string
	}{
		{"StatusQueued", StatusQueued, "queued"},
		{"StatusInProgress", StatusInProgress, "in_progress"},
		{"StatusCompleted", StatusCompleted, "completed"},
		{"StatusCanceled", StatusCanceled, "canceled"},
		{"StatusFailed", StatusFailed, "failed"},
		{"ActionQueued", ActionQueued, "queued"},
		{"ActionInProgress", ActionInProgress, "in_progress"},
		{"ActionCompleted", ActionCompleted, "completed"},
		{"ActionCanceled", ActionCanceled, "canceled"},
		{"ActionCancel", ActionCancel, "cancel"},
		{"ActionFinish", ActionFinish, "finish"},
		{"ActionPaused", ActionPaused, "paused"},
	}
	for _, p := range pairs {
		if p.got != p.want {
			t.Errorf("%s = %q, want %q", p.name, p.got, p.want)
		}
	}
}
