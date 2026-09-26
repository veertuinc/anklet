package github

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/anka"
	internalGithub "github.com/veertuinc/anklet/internal/github"
)

type receiverDecodeResult struct {
	Job           internalGithub.QueueJob
	IsWorkflowJob bool
}

func readWebhookPayload(r *http.Request, secret string) ([]byte, error) {
	if r == nil {
		return nil, fmt.Errorf("request is nil")
	}
	if r.Body == nil {
		return nil, fmt.Errorf("request body is empty")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return raw, fmt.Errorf("reading request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if _, err := github.ValidatePayload(r, []byte(secret)); err != nil {
		return raw, err
	}
	return raw, nil
}

func writeMalformedWebhook(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusBadRequest)
}

func decodeReceiverWebhook(r *http.Request, secret string) (receiverDecodeResult, []byte, error) {
	payload, err := readWebhookPayload(r, secret)
	if err != nil {
		return receiverDecodeResult{}, payload, fmt.Errorf("validating payload: %w", err)
	}
	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		return receiverDecodeResult{}, payload, fmt.Errorf("parsing event: %w", err)
	}
	workflowJob, ok := event.(*github.WorkflowJobEvent)
	if !ok {
		return receiverDecodeResult{}, payload, nil
	}
	job, err := queueJobFromWorkflowJobEvent(workflowJob)
	if err != nil {
		return receiverDecodeResult{}, payload, err
	}
	return receiverDecodeResult{Job: job, IsWorkflowJob: true}, payload, nil
}

func queueJobFromWorkflowJobEvent(event *github.WorkflowJobEvent) (internalGithub.QueueJob, error) {
	if event == nil {
		return internalGithub.QueueJob{}, fmt.Errorf("workflow job event is nil")
	}
	if event.Action == nil || *event.Action == "" {
		return internalGithub.QueueJob{}, fmt.Errorf("action is missing")
	}
	if event.WorkflowJob == nil {
		return internalGithub.QueueJob{}, fmt.Errorf("workflow_job is missing")
	}
	if event.WorkflowJob.ID == nil {
		return internalGithub.QueueJob{}, fmt.Errorf("workflow_job.id is missing")
	}
	return internalGithub.QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{
			ID:           event.WorkflowJob.ID,
			Name:         event.WorkflowJob.Name,
			RunID:        event.WorkflowJob.RunID,
			Status:       event.WorkflowJob.Status,
			Conclusion:   event.WorkflowJob.Conclusion,
			StartedAt:    event.WorkflowJob.StartedAt,
			CompletedAt:  event.WorkflowJob.CompletedAt,
			Labels:       event.WorkflowJob.Labels,
			HTMLURL:      event.WorkflowJob.HTMLURL,
			WorkflowName: event.WorkflowJob.WorkflowName,
		},
		Action:     *event.Action,
		Repository: repositoryFromEvent(event.Repo),
		AnkaVM:     anka.VM{},
		Attempts:   0,
	}, nil
}

func repositoryFromEvent(repo *github.Repository) internalGithub.Repository {
	if repo == nil {
		return internalGithub.Repository{}
	}
	out := internalGithub.Repository{
		Name:       repo.Name,
		Visibility: repo.Visibility,
		Private:    repo.Private,
	}
	if repo.Owner != nil {
		out.Owner = repo.Owner.Login
	}
	return out
}
