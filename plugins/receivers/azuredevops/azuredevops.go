package azuredevops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

// Azure DevOps webhook payload types

// WebhookPayload represents the top-level Azure DevOps webhook payload
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

// Message represents a message in the webhook payload
type Message struct {
	Text     string `json:"text"`
	HTML     string `json:"html"`
	Markdown string `json:"markdown"`
}

// Resource represents the resource in the webhook payload
type Resource struct {
	ProjectID    string       `json:"projectId"`
	Job          Job          `json:"job"`
	Stage        Stage        `json:"stage"`
	Run          PipelineRun  `json:"run"`
	Pipeline     Pipeline     `json:"pipeline"`
	Repositories []Repository `json:"repositories"`
}

// Links represents _links in various objects
type Links struct {
	Web         Link `json:"web"`
	PipelineWeb Link `json:"pipeline.web"`
	Self        Link `json:"self"`
	Pipeline    Link `json:"pipeline"`
}

// Link represents a single link
type Link struct {
	Href string `json:"href"`
}

// Job represents a job in the webhook payload
type Job struct {
	Links      Links      `json:"_links"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Attempt    int        `json:"attempt"`
	State      string     `json:"state"`            // waiting, inProgress, completed
	Result     string     `json:"result,omitempty"` // succeeded, failed, canceled
	StartTime  *time.Time `json:"startTime"`
	FinishTime *time.Time `json:"finishTime"`
}

// Stage represents a stage in the webhook payload
type Stage struct {
	Links       Links      `json:"_links"`
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Attempt     int        `json:"attempt"`
	State       string     `json:"state"`
	StartTime   *time.Time `json:"startTime"`
}

// PipelineRun represents a run/build in the webhook payload
type PipelineRun struct {
	Links              Links          `json:"_links"`
	TemplateParameters map[string]any `json:"templateParameters"`
	Pipeline           Pipeline       `json:"pipeline"`
	State              string         `json:"state"`
	CreatedDate        time.Time      `json:"createdDate"`
	URL                string         `json:"url"`
	Resources          RunResources   `json:"resources"`
	ID                 int            `json:"id"`
	Name               string         `json:"name"`
}

// RunResources represents resources in a run
type RunResources struct {
	Repositories map[string]RepositoryResource `json:"repositories"`
}

// RepositoryResource represents a repository resource in a run
type RepositoryResource struct {
	Repository struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"repository"`
	RefName string `json:"refName"`
	Version string `json:"version"`
}

// Pipeline represents a pipeline in the webhook payload
type Pipeline struct {
	URL      string `json:"url"`
	ID       int    `json:"id"`
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	Folder   string `json:"folder"`
}

// Repository represents a repository in the webhook payload
type Repository struct {
	Alias  string           `json:"alias"`
	ID     string           `json:"id"`
	Type   string           `json:"type"`
	Change RepositoryChange `json:"change"`
	URL    string           `json:"url"`
}

// RepositoryChange represents a change in a repository
type RepositoryChange struct {
	Author    Person `json:"author"`
	Committer Person `json:"committer"`
	Message   string `json:"message"`
	Version   string `json:"version"`
}

// Person represents a person (author/committer)
type Person struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// ResourceContainers represents resource containers in the webhook payload
type ResourceContainers struct {
	Collection Container `json:"collection"`
	Account    Container `json:"account"`
	Project    Container `json:"project"`
}

// Container represents a container (collection, account, project)
type Container struct {
	ID      string `json:"id"`
	BaseURL string `json:"baseUrl"`
}

// QueueJob represents a job queued for processing by Anklet
type QueueJob struct {
	Type      string      `json:"type"`
	Job       Job         `json:"job"`
	Run       PipelineRun `json:"run"`
	Pipeline  Pipeline    `json:"pipeline"`
	ProjectID string      `json:"project_id"`
	OrgURL    string      `json:"org_url"`
	Action    string      `json:"action"` // waiting, inProgress, completed
	AnkaVM    anka.VM     `json:"anka_vm"`
	Attempts  int         `json:"attempts"`
	PausedOn  string      `json:"paused_on,omitempty"`
}

// Server defines the structure for the API server
type Server struct {
	Port string
}

// NewServer creates a new instance of Server
func NewServer(port string) *Server {
	return &Server{
		Port: port,
	}
}

// GetJobJSONFromQueueByID searches for a job in a queue by job ID
func GetJobJSONFromQueueByID(ctx context.Context, jobID string, queueName string) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return "", err
	}
	queuedJobs, err := databaseContainer.RetryLRange(ctx, queueName, 0, -1)
	if err != nil {
		return "", err
	}
	for _, queuedJobJSON := range queuedJobs {
		var queuedJob QueueJob
		err := json.Unmarshal([]byte(queuedJobJSON), &queuedJob)
		if err != nil {
			continue
		}
		if queuedJob.Job.ID == jobID {
			return queuedJobJSON, nil
		}
	}
	return "", nil
}

// mapAzureDevOpsStateToAction maps Azure DevOps job states to action names
func mapAzureDevOpsStateToAction(state string) string {
	switch state {
	case "waiting":
		return "queued"
	case "inProgress":
		return "in_progress"
	case "completed":
		return "completed"
	default:
		return state
	}
}

// Run starts the HTTP server for receiving Azure DevOps webhooks
func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
) (context.Context, error) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}
	configFileName, err := config.GetConfigFileNameFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	if pluginConfig.Token == "" {
		return pluginCtx, fmt.Errorf("token is not set in %s:plugins:%s<token>", configFileName, pluginConfig.Name)
	}
	if pluginConfig.Owner == "" {
		return pluginCtx, fmt.Errorf("owner (organization) is not set in %s:plugins:%s<owner>", configFileName, pluginConfig.Name)
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: %s", err.Error())
	}

	// clean up in_progress queue if it exists
	_, err = databaseContainer.RetryDel(pluginCtx, "anklet/jobs/azuredevops/in_progress/"+pluginConfig.Owner)
	if err != nil {
		return pluginCtx, fmt.Errorf("error deleting in_progress queue: %s", err.Error())
	}

	server := &http.Server{Addr: ":" + pluginConfig.Port}
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("ok"))
		if err != nil {
			logging.Error(pluginCtx, "error writing response", "error", err)
		}
	})

	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			logging.Error(pluginCtx, "error reading request body", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() {
			if closeErr := r.Body.Close(); closeErr != nil {
				logging.Error(pluginCtx, "error closing request body", "error", closeErr)
			}
		}()

		// Parse the webhook payload
		var webhookPayload WebhookPayload
		err = json.Unmarshal(body, &webhookPayload)
		if err != nil {
			logging.Error(pluginCtx, "error parsing webhook payload", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Only handle job state changed events
		if webhookPayload.EventType != "ms.vss-pipelines.job-state-changed-event" {
			logging.Debug(pluginCtx, "ignoring non-job-state-changed event", "eventType", webhookPayload.EventType)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Create a queue job from the webhook payload
		queueJob := QueueJob{
			Type:      "AzureDevOpsJob",
			Job:       webhookPayload.Resource.Job,
			Run:       webhookPayload.Resource.Run,
			Pipeline:  webhookPayload.Resource.Pipeline,
			ProjectID: webhookPayload.Resource.ProjectID,
			OrgURL:    webhookPayload.ResourceContainers.Account.BaseURL,
			Action:    mapAzureDevOpsStateToAction(webhookPayload.Resource.Job.State),
			AnkaVM:    anka.VM{},
			Attempts:  0,
		}

		// Create a fresh context for this webhook request
		webhookCtx := logging.AppendCtx(pluginCtx, slog.Group("job",
			slog.String("jobID", queueJob.Job.ID),
			slog.String("jobName", queueJob.Job.Name),
			slog.String("jobState", queueJob.Job.State),
			slog.Int("runID", queueJob.Run.ID),
			slog.String("runName", queueJob.Run.Name),
			slog.String("pipelineName", queueJob.Pipeline.Name),
			slog.String("projectID", queueJob.ProjectID),
			slog.String("action", queueJob.Action),
		))
		webhookCtx = logging.AppendCtx(webhookCtx, slog.String("notificationID", fmt.Sprintf("%d", webhookPayload.NotificationID)))

		logging.Info(webhookCtx, "received azure devops job webhook")

		jobState := webhookPayload.Resource.Job.State

		if jobState == "waiting" {
			// Job is queued/waiting - add to queue
			queuedQueueName := "anklet/jobs/azuredevops/queued/" + pluginConfig.Owner
			inQueueJobJSON, err := GetJobJSONFromQueueByID(pluginCtx, queueJob.Job.ID, queuedQueueName)
			if err != nil {
				logging.Error(webhookCtx, "error searching in queue", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if inQueueJobJSON == "" {
				// Push to queue
				wrappedPayloadJSON, err := json.Marshal(queueJob)
				if err != nil {
					logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, queuedQueueName, wrappedPayloadJSON)
				if pushErr != nil {
					logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				logging.Info(webhookCtx, "job pushed to queued queue", "queue", queuedQueueName, "queue_length", queueLength)
			} else {
				logging.Warn(webhookCtx, "job already present in queued queue, skipping enqueue", "queue", queuedQueueName)
			}

		} else if jobState == "inProgress" {
			// Job is in progress
			inProgressQueueName := "anklet/jobs/azuredevops/in_progress/" + pluginConfig.Owner
			inQueueJobJSON, err := GetJobJSONFromQueueByID(pluginCtx, queueJob.Job.ID, inProgressQueueName)
			if err != nil {
				logging.Error(webhookCtx, "error searching in queue", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if inQueueJobJSON == "" {
				wrappedPayloadJSON, err := json.Marshal(queueJob)
				if err != nil {
					logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, inProgressQueueName, wrappedPayloadJSON)
				if pushErr != nil {
					logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				logging.Info(webhookCtx, "job pushed to in_progress queue", "queue", inProgressQueueName, "queue_length", queueLength)
			} else {
				logging.Debug(webhookCtx, "job already present in in_progress queue, skipping enqueue", "queue", inProgressQueueName)
			}

		} else if jobState == "completed" {
			// Job is completed
			// Check if it exists in any queued queue first
			queuedKeys, err := databaseContainer.RetryKeys(pluginCtx, "anklet/jobs/azuredevops/queued/"+pluginConfig.Owner+"*")
			if err != nil {
				logging.Error(webhookCtx, "error getting list of queued keys", "error", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			inAQueue := false
			for _, queue := range queuedKeys {
				inQueueJobJSON, err := GetJobJSONFromQueueByID(pluginCtx, queueJob.Job.ID, queue)
				if err != nil {
					logging.Warn(webhookCtx, "error searching in queue", "error", err, "queue", queue)
					continue
				}
				if inQueueJobJSON != "" {
					inAQueue = true
					break
				}
			}

			if inAQueue {
				completedQueueName := "anklet/jobs/azuredevops/completed/" + pluginConfig.Owner
				inCompletedQueueJobJSON, err := GetJobJSONFromQueueByID(pluginCtx, queueJob.Job.ID, completedQueueName)
				if err != nil {
					logging.Error(webhookCtx, "error searching in completed queue", "error", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				if inCompletedQueueJobJSON == "" {
					wrappedPayloadJSON, err := json.Marshal(queueJob)
					if err != nil {
						logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, completedQueueName, wrappedPayloadJSON)
					if pushErr != nil {
						logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					logging.Info(webhookCtx, "job pushed to completed queue", "queue", completedQueueName, "queue_length", queueLength)
				} else {
					logging.Debug(webhookCtx, "job already present in completed queue, skipping enqueue", "queue", completedQueueName)
				}
			} else {
				logging.Debug(webhookCtx, "job not present in any tracked queue, skipping completed enqueue",
					"job_id", queueJob.Job.ID,
					"queues_checked", queuedKeys,
				)
			}
		}

		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, err := w.Write([]byte("please use /jobs/v1"))
		if err != nil {
			logging.Error(pluginCtx, "error writing response", "error", err)
		}
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error(pluginCtx, "receiver listener error", "error", err)
		}
	}()

	err = metrics.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
		Status:      "running",
		StatusSince: time.Now(),
	})
	if err != nil {
		return pluginCtx, fmt.Errorf("error updating plugin metrics: %s", err.Error())
	}

	// notify the main thread that the service has started
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Store(true)
	logging.Info(pluginCtx, "azure devops receiver finished starting")

	// wait for the context to be canceled
	for {
		select {
		case <-workerCtx.Done():
			logging.Warn(pluginCtx, "shutting down receiver")
			if err := server.Shutdown(pluginCtx); err != nil {
				return pluginCtx, fmt.Errorf("receiver shutdown error: %s", err.Error())
			}
			return pluginCtx, nil
		case <-time.After(time.Second * 1):
			continue
		}
	}
}
