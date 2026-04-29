package azure_devops

import (
	"time"

	"github.com/veertuinc/anklet/internal/jobqueue"
)

// WebhookPayload is the top-level body Azure DevOps sends to a service hook
// subscription. Anklet only acts on `ms.vss-pipelines.job-state-changed-event`
// publications, but the struct is shared with all hook payloads.
type WebhookPayload struct {
	SubscriptionID     string             `json:"subscriptionId"`
	NotificationID     int                `json:"notificationId"`
	ID                 string             `json:"id"`
	EventType          string             `json:"eventType"`
	PublisherID        string             `json:"publisherId"`
	Message            Message            `json:"message"`
	DetailedMessage    Message            `json:"detailedMessage"`
	Resource           Resource           `json:"resource"`
	ResourceVersion    string             `json:"resourceVersion"`
	ResourceContainers ResourceContainers `json:"resourceContainers"`
	CreatedDate        time.Time          `json:"createdDate"`
}

// Message holds the human-friendly summary of an event in three formats.
type Message struct {
	Text     string `json:"text"`
	HTML     string `json:"html"`
	Markdown string `json:"markdown"`
}

// Resource is the publisher-specific payload section of a webhook. For
// pipeline events it carries the job, run, pipeline, project, and repos.
type Resource struct {
	ProjectID    string       `json:"projectId"`
	Job          Job          `json:"job"`
	Stage        Stage        `json:"stage"`
	Run          PipelineRun  `json:"run"`
	Pipeline     Pipeline     `json:"pipeline"`
	Repositories []Repository `json:"repositories"`
}

// Links is the standard Azure DevOps `_links` envelope.
type Links struct {
	Web         Link `json:"web"`
	PipelineWeb Link `json:"pipeline.web"`
	Self        Link `json:"self"`
	Pipeline    Link `json:"pipeline"`
}

// Link is a single hyperlink in a `_links` envelope.
type Link struct {
	Href string `json:"href"`
}

// Job is one job inside a pipeline run. State is "waiting" | "inProgress" |
// "completed"; Result is set on completion as "succeeded" | "failed" |
// "canceled".
type Job struct {
	Links      Links      `json:"_links"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Attempt    int        `json:"attempt"`
	State      string     `json:"state"`
	Result     string     `json:"result,omitempty"`
	StartTime  *time.Time `json:"startTime"`
	FinishTime *time.Time `json:"finishTime"`
}

// Stage is one stage inside a pipeline run.
type Stage struct {
	Links       Links      `json:"_links"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Attempt     int        `json:"attempt"`
	State       string     `json:"state"`
	StartTime   *time.Time `json:"startTime"`
}

// PipelineRun is the build / run that owns a job. ID maps to the integer
// buildId used by the build API for cancellation.
type PipelineRun struct {
	Links       Links        `json:"_links"`
	Pipeline    Pipeline     `json:"pipeline"`
	State       string       `json:"state"`
	CreatedDate time.Time    `json:"createdDate"`
	URL         string       `json:"url"`
	Resources   RunResources `json:"resources"`
	ID          int          `json:"id"`
	Name        string       `json:"name"`
}

// RunResources groups the resources a run uses; today only repositories.
type RunResources struct {
	Repositories map[string]RepositoryResource `json:"repositories"`
}

// RepositoryResource is a repo a run depends on, keyed by alias.
type RepositoryResource struct {
	Repository struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"repository"`
	RefName string `json:"refName"`
	Version string `json:"version"`
}

// Pipeline is the pipeline definition that produced the run.
type Pipeline struct {
	URL      string `json:"url"`
	ID       int    `json:"id"`
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	Folder   string `json:"folder"`
}

// Repository is a repo described in a webhook payload.
type Repository struct {
	Alias  string           `json:"alias"`
	ID     string           `json:"id"`
	Type   string           `json:"type"`
	Change RepositoryChange `json:"change"`
	URL    string           `json:"url"`
}

// RepositoryChange is the commit metadata accompanying a repository.
type RepositoryChange struct {
	Author    Person `json:"author"`
	Committer Person `json:"committer"`
	Message   string `json:"message"`
	Version   string `json:"version"`
}

// Person is an author / committer attribution.
type Person struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// ResourceContainers carries the org / project / collection IDs the event
// belongs to.
type ResourceContainers struct {
	Collection Container `json:"collection"`
	Account    Container `json:"account"`
	Project    Container `json:"project"`
}

// Container is a collection / account / project reference.
type Container struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
}

// QueueJob is the Azure DevOps payload Anklet pushes onto the Redis queues.
// It embeds jobqueue.BaseQueueJob so the Type/Action/Attempts/PausedOn/AnkaVM
// fields are shared with the GitHub plugin and any future generic helpers.
type QueueJob struct {
	jobqueue.BaseQueueJob
	Job       Job         `json:"job"`
	Run       PipelineRun `json:"run"`
	Pipeline  Pipeline    `json:"pipeline"`
	ProjectID string      `json:"project_id"`
	OrgURL    string      `json:"org_url"`
}

// Matches reports whether other refers to the same Azure DevOps job (same
// pipeline run + same job ID + same attempt). Designed to be passed as the
// Matcher argument to jobqueue.UpdateJobInDB.
func (q QueueJob) Matches(other QueueJob) bool {
	if q.Type != other.Type {
		return false
	}
	if q.Job.ID == "" || other.Job.ID == "" {
		return false
	}
	return q.Job.ID == other.Job.ID &&
		q.Run.ID == other.Run.ID &&
		q.Job.Attempt == other.Job.Attempt
}

// IsCompleted reports whether the queued job has reached a terminal state per
// Azure DevOps' own state vocabulary. Designed to be passed as the
// CompletedPredicate argument to jobqueue.CheckIfJobIsCompleted.
func (q QueueJob) IsCompleted() bool {
	return q.Job.State == JobStateCompleted
}

// MapJobStateToAction translates Azure DevOps' (state, result) pair from a
// job-state-changed webhook into the normalized jobqueue status vocabulary.
//
// Azure DevOps states:
//   - "waiting"    => StatusQueued
//   - "inProgress" => StatusInProgress
//   - "completed"  => combine with Result:
//       "canceled"  => StatusCanceled
//       "failed"    => StatusFailed
//       "succeeded" => StatusCompleted
//       (anything else, including empty) => StatusCompleted
//
// Anything unrecognized falls back to StatusInProgress so we never lose
// track of an in-flight job because of an unmapped state.
func MapJobStateToAction(state, result string) string {
	switch state {
	case JobStateWaiting:
		return jobqueue.StatusQueued
	case JobStateInProgress:
		return jobqueue.StatusInProgress
	case JobStateCompleted:
		switch result {
		case JobResultCanceled:
			return jobqueue.StatusCanceled
		case JobResultFailed:
			return jobqueue.StatusFailed
		default:
			return jobqueue.StatusCompleted
		}
	default:
		return jobqueue.StatusInProgress
	}
}

// Native Azure DevOps state / result strings, kept here so the plugin code
// can compare without sprinkling the literals around.
const (
	JobStateWaiting    = "waiting"
	JobStateInProgress = "inProgress"
	JobStateCompleted  = "completed"

	JobResultSucceeded = "succeeded"
	JobResultFailed    = "failed"
	JobResultCanceled  = "canceled"
)

// JobStateChangedEvent is the eventType webhook subscriptions deliver when a
// pipeline job transitions states.
const JobStateChangedEvent = "ms.vss-pipelines.job-state-changed-event"
