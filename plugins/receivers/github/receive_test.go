package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v74/github"
)

func signedReceiverRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte("secret"))
	_, err := mac.Write([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/jobs/v1/receiver", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

func validWorkflowJobEvent() *gogithub.WorkflowJobEvent {
	return &gogithub.WorkflowJobEvent{
		Action: gogithub.Ptr("queued"),
		WorkflowJob: &gogithub.WorkflowJob{
			ID:           gogithub.Ptr(int64(123)),
			Name:         gogithub.Ptr("build"),
			RunID:        gogithub.Ptr(int64(456)),
			Status:       gogithub.Ptr("queued"),
			Labels:       []string{"anka-template:macos-14"},
			HTMLURL:      gogithub.Ptr("https://github.com/org/repo/actions/runs/456/job/123"),
			WorkflowName: gogithub.Ptr("CI"),
		},
		Repo: &gogithub.Repository{
			Name:    gogithub.Ptr("repo"),
			Private: gogithub.Ptr(true),
			Owner: &gogithub.User{
				Login: gogithub.Ptr("org"),
			},
		},
	}
}

func TestQueueJobFromWorkflowJobEvent(t *testing.T) {
	t.Parallel()

	t.Run("valid event", func(t *testing.T) {
		t.Parallel()
		got, err := queueJobFromWorkflowJobEvent(validWorkflowJobEvent())
		if err != nil {
			t.Fatalf("queueJobFromWorkflowJobEvent() error = %v", err)
		}
		if got.Type != "WorkflowJobPayload" {
			t.Errorf("Type = %q, want WorkflowJobPayload", got.Type)
		}
		if got.Action != "queued" {
			t.Errorf("Action = %q, want queued", got.Action)
		}
		if got.WorkflowJob.ID == nil || *got.WorkflowJob.ID != 123 {
			t.Errorf("WorkflowJob.ID = %v, want 123", got.WorkflowJob.ID)
		}
		if got.Repository.Name == nil || *got.Repository.Name != "repo" {
			t.Errorf("Repository.Name = %v, want repo", got.Repository.Name)
		}
		if got.Repository.Owner == nil || *got.Repository.Owner != "org" {
			t.Errorf("Repository.Owner = %v, want org", got.Repository.Owner)
		}
	})

	tests := []struct {
		name    string
		event   *gogithub.WorkflowJobEvent
		wantErr string
	}{
		{
			name:    "nil event",
			event:   nil,
			wantErr: "workflow job event is nil",
		},
		{
			name: "nil workflow_job",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.WorkflowJob = nil
				return e
			}(),
			wantErr: "workflow_job is missing",
		},
		{
			name: "nil action",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Action = nil
				return e
			}(),
			wantErr: "action is missing",
		},
		{
			name: "nil workflow_job id",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.WorkflowJob.ID = nil
				return e
			}(),
			wantErr: "workflow_job.id is missing",
		},
		{
			name: "empty action",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Action = gogithub.Ptr("")
				return e
			}(),
			wantErr: "action is missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := queueJobFromWorkflowJobEvent(tt.event)
			if err == nil {
				t.Fatal("queueJobFromWorkflowJobEvent() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestQueueJobFromWorkflowJobEventAcceptsOptionalFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		event *gogithub.WorkflowJobEvent
	}{
		{
			name: "nil repository",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Repo = nil
				return e
			}(),
		},
		{
			name: "nil repository owner",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Repo.Owner = nil
				return e
			}(),
		},
		{
			name: "empty repository owner login",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Repo.Owner.Login = gogithub.Ptr("")
				return e
			}(),
		},
		{
			name: "nil repository name",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.Repo.Name = nil
				return e
			}(),
		},
		{
			name: "nil workflow_job run_id",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.WorkflowJob.RunID = nil
				return e
			}(),
		},
		{
			name: "nil workflow_job name",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.WorkflowJob.Name = nil
				return e
			}(),
		},
		{
			name: "nil workflow_job status",
			event: func() *gogithub.WorkflowJobEvent {
				e := validWorkflowJobEvent()
				e.WorkflowJob.Status = nil
				return e
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := queueJobFromWorkflowJobEvent(tt.event)
			if err != nil {
				t.Fatalf("queueJobFromWorkflowJobEvent() error = %v", err)
			}
			if got.Action != "queued" {
				t.Errorf("Action = %q, want queued", got.Action)
			}
			if got.WorkflowJob.ID == nil || *got.WorkflowJob.ID != 123 {
				t.Errorf("WorkflowJob.ID = %v, want 123", got.WorkflowJob.ID)
			}
		})
	}
}

func TestDecodeReceiverWebhookRejectsMalformedPayload(t *testing.T) {
	t.Parallel()
	body := `{"action":"queued"}`
	req := signedReceiverRequest(t, body)
	_, raw, err := decodeReceiverWebhook(req, "secret")
	if err == nil {
		t.Fatal("decodeReceiverWebhook() error = nil, want malformed payload error")
	}
	if string(raw) != body {
		t.Errorf("raw payload = %q, want %q", raw, body)
	}
	rec := httptest.NewRecorder()
	writeMalformedWebhook(rec, err)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDecodeReceiverWebhookIgnoresNonWorkflowJobEvent(t *testing.T) {
	t.Parallel()
	body := `{"zen":"keep it logically awesome","hook_id":1}`
	req := signedReceiverRequest(t, body)
	req.Header.Set("X-GitHub-Event", "ping")
	got, _, err := decodeReceiverWebhook(req, "secret")
	if err != nil {
		t.Fatalf("decodeReceiverWebhook() error = %v", err)
	}
	if got.IsWorkflowJob {
		t.Fatal("ping event must not be treated as a workflow_job")
	}
}

func TestDecodeReceiverWebhookAcceptsOptionalFieldsOmitted(t *testing.T) {
	t.Parallel()
	req := signedReceiverRequest(t, `{"action":"queued","workflow_job":{"id":123}}`)
	got, _, err := decodeReceiverWebhook(req, "secret")
	if err != nil {
		t.Fatalf("decodeReceiverWebhook() error = %v", err)
	}
	if !got.IsWorkflowJob {
		t.Fatal("IsWorkflowJob = false, want true")
	}
	if got.Job.Repository.Owner != nil {
		t.Errorf("Repository.Owner = %v, want nil", got.Job.Repository.Owner)
	}
}

func TestDecodeReceiverWebhookAcceptsCompletePayload(t *testing.T) {
	t.Parallel()
	body := `{
		"action":"queued",
		"workflow_job":{"id":123,"run_id":456,"name":"build","status":"queued","labels":["anka-template:macos-14"]},
		"repository":{"name":"repo","owner":{"login":"org"},"private":true}
	}`
	req := signedReceiverRequest(t, body)
	got, _, err := decodeReceiverWebhook(req, "secret")
	if err != nil {
		t.Fatalf("decodeReceiverWebhook() error = %v", err)
	}
	if !got.IsWorkflowJob {
		t.Fatal("IsWorkflowJob = false, want true")
	}
	if got.Job.Action != "queued" {
		t.Errorf("Action = %q, want queued", got.Job.Action)
	}
}

func TestQueueJobFromParsedIncompleteWorkflowJobPayload(t *testing.T) {
	t.Parallel()
	event, err := gogithub.ParseWebHook("workflow_job", []byte(`{"action":"queued"}`))
	if err != nil {
		t.Fatalf("ParseWebHook() error = %v", err)
	}
	workflowJob, ok := event.(*gogithub.WorkflowJobEvent)
	if !ok {
		t.Fatalf("ParseWebHook type = %T, want *WorkflowJobEvent", event)
	}
	_, err = queueJobFromWorkflowJobEvent(workflowJob)
	if err == nil {
		t.Fatal("incomplete workflow_job payload must return an error, not panic")
	}
}

func TestReadWebhookPayloadRejectsNilBody(t *testing.T) {
	t.Parallel()
	req := &http.Request{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   nil,
	}
	_, err := readWebhookPayload(req, "secret")
	if err == nil {
		t.Fatal("readWebhookPayload() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "request body is empty") {
		t.Errorf("error = %q, want substring %q", err.Error(), "request body is empty")
	}
}

func TestReadWebhookPayloadRejectsInvalidSignature(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest(http.MethodPost, "/jobs/v1/receiver", bytes.NewBufferString(`{"action":"queued"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	raw, err := readWebhookPayload(req, "secret")
	if err == nil {
		t.Fatal("readWebhookPayload() error = nil, want error")
	}
	if string(raw) != `{"action":"queued"}` {
		t.Errorf("raw payload = %q, want %q", raw, `{"action":"queued"}`)
	}
}

func TestQueueJobJSONMatchesRedisShape(t *testing.T) {
	t.Parallel()
	job, err := queueJobFromWorkflowJobEvent(validWorkflowJobEvent())
	if err != nil {
		t.Fatalf("queueJobFromWorkflowJobEvent() error = %v", err)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got["type"] != "WorkflowJobPayload" {
		t.Errorf("type = %v, want WorkflowJobPayload", got["type"])
	}
	if got["action"] != "queued" {
		t.Errorf("action = %v, want queued", got["action"])
	}
	if got["attempts"] != float64(0) {
		t.Errorf("attempts = %v, want 0", got["attempts"])
	}
	repo, ok := got["repository"].(map[string]any)
	if !ok {
		t.Fatalf("repository type = %T, want object", got["repository"])
	}
	if repo["owner"] != "org" {
		t.Errorf("repository.owner = %#v, want string %q", repo["owner"], "org")
	}
	if repo["name"] != "repo" {
		t.Errorf("repository.name = %v, want repo", repo["name"])
	}
	workflowJob, ok := got["workflow_job"].(map[string]any)
	if !ok {
		t.Fatalf("workflow_job type = %T, want object", got["workflow_job"])
	}
	if workflowJob["id"] != float64(123) {
		t.Errorf("workflow_job.id = %v, want 123", workflowJob["id"])
	}
}

func TestQueueJobJSONAllowsNullOptionalFields(t *testing.T) {
	t.Parallel()
	event := validWorkflowJobEvent()
	event.Repo = nil
	event.WorkflowJob.RunID = nil
	event.WorkflowJob.Name = nil
	event.WorkflowJob.Status = nil
	job, err := queueJobFromWorkflowJobEvent(event)
	if err != nil {
		t.Fatalf("queueJobFromWorkflowJobEvent() error = %v", err)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	repo, ok := got["repository"].(map[string]any)
	if !ok {
		t.Fatalf("repository type = %T, want object", got["repository"])
	}
	if repo["owner"] != nil {
		t.Errorf("repository.owner = %#v, want null", repo["owner"])
	}
	if repo["name"] != nil {
		t.Errorf("repository.name = %#v, want null", repo["name"])
	}
	workflowJob, ok := got["workflow_job"].(map[string]any)
	if !ok {
		t.Fatalf("workflow_job type = %T, want object", got["workflow_job"])
	}
	if workflowJob["id"] != float64(123) {
		t.Errorf("workflow_job.id = %v, want 123", workflowJob["id"])
	}
	if workflowJob["run_id"] != nil {
		t.Errorf("workflow_job.run_id = %#v, want null", workflowJob["run_id"])
	}
	if workflowJob["name"] != nil {
		t.Errorf("workflow_job.name = %#v, want null", workflowJob["name"])
	}
	if workflowJob["status"] != nil {
		t.Errorf("workflow_job.status = %#v, want null", workflowJob["status"])
	}
}
