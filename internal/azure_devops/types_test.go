package azure_devops

import (
	"encoding/json"
	"testing"

	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/jobqueue"
)

func TestQueueJob_JSONRoundTrip(t *testing.T) {
	original := QueueJob{
		BaseQueueJob: jobqueue.BaseQueueJob{
			Type:     "AzureDevOpsJob",
			Action:   jobqueue.ActionInProgress,
			Attempts: 1,
			PausedOn: "host-2",
			AnkaVM:   anka.VM{Name: "vm-x", CPUCount: 8, MEMBytes: 16 << 30},
		},
		Job: Job{
			ID:      "abc-123",
			Name:    "Job",
			State:   JobStateInProgress,
			Attempt: 1,
		},
		Run: PipelineRun{
			ID:    42,
			Name:  "20260428.8",
			State: "inProgress",
		},
		Pipeline:  Pipeline{ID: 3, Name: "anklet-azure-devops-examples"},
		ProjectID: "8faeacdc-9dcd-4192-bb8d-41eb0a516cf4",
		OrgURL:    "https://dev.azure.com/veertu/",
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped QueueJob
	if err := json.Unmarshal(raw, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if roundTripped.Job != original.Job {
		t.Errorf("job mismatch: got %+v, want %+v", roundTripped.Job, original.Job)
	}
	if roundTripped.ProjectID != original.ProjectID || roundTripped.OrgURL != original.OrgURL {
		t.Errorf("project/org mismatch: got (%q, %q), want (%q, %q)",
			roundTripped.ProjectID, roundTripped.OrgURL, original.ProjectID, original.OrgURL)
	}
	if roundTripped.Action != original.Action || roundTripped.Type != original.Type {
		t.Errorf("base fields mismatch: got (%q, %q), want (%q, %q)",
			roundTripped.Action, roundTripped.Type, original.Action, original.Type)
	}
}

func TestQueueJob_Matches(t *testing.T) {
	a := QueueJob{
		BaseQueueJob: jobqueue.BaseQueueJob{Type: "AzureDevOpsJob"},
		Job:          Job{ID: "abc", Attempt: 1},
		Run:          PipelineRun{ID: 1},
	}
	b := a // exact copy
	if !a.Matches(b) {
		t.Errorf("expected exact copy to match")
	}
	c := a
	c.Job.Attempt = 2
	if a.Matches(c) {
		t.Errorf("attempt mismatch should not match")
	}
	d := a
	d.Run.ID = 99
	if a.Matches(d) {
		t.Errorf("run ID mismatch should not match")
	}
	e := a
	e.Job.ID = ""
	if a.Matches(e) {
		t.Errorf("empty Job.ID must not match anything")
	}
}

func TestQueueJob_IsCompleted(t *testing.T) {
	if (QueueJob{Job: Job{State: JobStateInProgress}}).IsCompleted() {
		t.Errorf("inProgress should not be completed")
	}
	if !(QueueJob{Job: Job{State: JobStateCompleted}}).IsCompleted() {
		t.Errorf("completed should be completed")
	}
}

func TestMapJobStateToAction(t *testing.T) {
	tests := []struct {
		state, result string
		want          string
	}{
		{JobStateWaiting, "", jobqueue.StatusQueued},
		{JobStateInProgress, "", jobqueue.StatusInProgress},
		{JobStateCompleted, JobResultSucceeded, jobqueue.StatusCompleted},
		{JobStateCompleted, JobResultFailed, jobqueue.StatusFailed},
		{JobStateCompleted, JobResultCanceled, jobqueue.StatusCanceled},
		{JobStateCompleted, "", jobqueue.StatusCompleted},
		{"unknown", "", jobqueue.StatusInProgress},
	}
	for _, tt := range tests {
		t.Run(tt.state+"_"+tt.result, func(t *testing.T) {
			got := MapJobStateToAction(tt.state, tt.result)
			if got != tt.want {
				t.Errorf("MapJobStateToAction(%q, %q) = %q, want %q", tt.state, tt.result, got, tt.want)
			}
		})
	}
}

func TestWebhookPayload_Parse(t *testing.T) {
	// Minimal real-shape payload (subset of an actual hook delivery, fields
	// the receiver consumes today).
	payload := []byte(`{
		"subscriptionId": "abc",
		"eventType": "ms.vss-pipelines.job-state-changed-event",
		"resource": {
			"projectId": "PROJ",
			"job": {"id": "JOB1", "name": "Job", "attempt": 1, "state": "waiting"},
			"run":  {"id": 30, "name": "20260428.8", "state": "inProgress"},
			"pipeline": {"id": 3, "name": "p"}
		},
		"resourceContainers": {
			"account": {"baseUrl": "https://dev.azure.com/veertu/"}
		}
	}`)
	var hook WebhookPayload
	if err := json.Unmarshal(payload, &hook); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if hook.EventType != JobStateChangedEvent {
		t.Errorf("EventType = %q, want %q", hook.EventType, JobStateChangedEvent)
	}
	if hook.Resource.Job.ID != "JOB1" || hook.Resource.Job.State != JobStateWaiting {
		t.Errorf("job parse mismatch: %+v", hook.Resource.Job)
	}
	if hook.Resource.Run.ID != 30 {
		t.Errorf("run.ID = %d, want 30", hook.Resource.Run.ID)
	}
	if hook.ResourceContainers.Account.BaseURL != "https://dev.azure.com/veertu/" {
		t.Errorf("account.baseUrl mismatch: %q", hook.ResourceContainers.Account.BaseURL)
	}
}
