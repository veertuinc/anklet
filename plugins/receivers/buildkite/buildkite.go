package buildkite

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	buildkiteSDK "github.com/buildkite/go-buildkite/v4"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	internalModels "github.com/veertuinc/anklet/internal/models"
	"github.com/veertuinc/anklet/internal/queue"
)

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

func existsInArrayPartial(arrayToSearchIn []string, desired []string) bool {
	for _, desired_string := range desired {
		found := false
		for _, item := range arrayToSearchIn {
			if strings.Contains(item, desired_string) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type webhookPayload struct {
	Event    string
	Action   string
	Job      buildkiteSDK.Job
	Build    buildkiteSDK.Build
	Pipeline buildkiteSDK.Pipeline
	JobEnv   map[string]string
}

func parseWebhook(r *http.Request, secret string) (webhookPayload, error) {
	defer func() {
		_ = r.Body.Close()
	}()
	var payload webhookPayload
	rawPayload, err := buildkiteSDK.ValidatePayload(r, []byte(secret))
	if err != nil {
		return payload, err
	}
	payload.Event = strings.TrimSpace(buildkiteSDK.WebHookType(r))
	if payload.Event == "" {
		return payload, fmt.Errorf("missing buildkite webhook event header")
	}
	parsedEvent, err := buildkiteSDK.ParseWebHook(payload.Event, rawPayload)
	if err != nil {
		return payload, err
	}
	switch event := parsedEvent.(type) {
	case *buildkiteSDK.JobScheduledEvent:
		payload.Action = "queued"
		payload.Job = event.Job
		payload.Build = event.Build
		payload.Pipeline = event.Pipeline
	case *buildkiteSDK.JobStartedEvent:
		payload.Action = "in_progress"
		payload.Job = event.Job
		payload.Build = event.Build
		payload.Pipeline = event.Pipeline
	case *buildkiteSDK.JobFinishedEvent:
		payload.Action = "completed"
		payload.Job = event.Job
		payload.Build = event.Build
		payload.Pipeline = event.Pipeline
	default:
		return payload, fmt.Errorf("unsupported buildkite webhook event: %s", payload.Event)
	}
	var envWrapper struct {
		Job struct {
			Env map[string]string `json:"env"`
		} `json:"job"`
	}
	if err := json.Unmarshal(rawPayload, &envWrapper); err == nil {
		payload.JobEnv = envWrapper.Job.Env
	}
	return payload, nil
}

func stableID(raw string) int64 {
	if raw == "" {
		return 0
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return v
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(raw))
	return int64(h.Sum64())
}

func ensureSelectorLabels(labels []string, env map[string]string, jobID string) []string {
	out := make([]string, 0, len(labels)+3)
	out = append(out, labels...)
	if jobID != "" {
		out = append(out, "buildkite-job-id:"+jobID)
	}
	hasTemplate := false
	hasTag := false
	for _, label := range out {
		if strings.HasPrefix(label, "anka-template:") {
			hasTemplate = true
		}
		if strings.HasPrefix(label, "anka-template-tag:") {
			hasTag = true
		}
	}
	if !hasTemplate && env["ANKA_TEMPLATE_UUID"] != "" {
		out = append(out, "anka-template:"+env["ANKA_TEMPLATE_UUID"])
	}
	if !hasTag && env["ANKA_TEMPLATE_TAG"] != "" {
		out = append(out, "anka-template-tag:"+env["ANKA_TEMPLATE_TAG"])
	}
	return out
}

func buildConclusion(state string, exitStatus any) *string {
	normalized := strings.ToLower(state)
	switch normalized {
	case "passed":
		v := "success"
		return &v
	case "failed", "failing":
		v := "failure"
		return &v
	case "canceled", "cancelled":
		v := "cancelled"
		return &v
	case "timed_out":
		v := "timed_out"
		return &v
	}
	switch v := exitStatus.(type) {
	case *int:
		if v != nil && *v == 0 {
			ok := "success"
			return &ok
		}
		if v != nil {
			fail := "failure"
			return &fail
		}
	case int:
		if v == 0 {
			ok := "success"
			return &ok
		}
		fail := "failure"
		return &fail
	case float64:
		if int(v) == 0 {
			ok := "success"
			return &ok
		}
		fail := "failure"
		return &fail
	case string:
		if v == "0" {
			ok := "success"
			return &ok
		}
	}
	return nil
}

func parseWebhookTime(raw *buildkiteSDK.Timestamp) *time.Time {
	if raw == nil {
		return nil
	}
	parsed := raw.Time
	return &parsed
}

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

	if pluginConfig.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in %s:plugins:%s<secret>", configFileName, pluginConfig.Name)
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: %s", err.Error())
	}

	queueOwner := pluginConfig.GetQueueOwner()

	server := &http.Server{Addr: ":" + pluginConfig.Port}
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("ok"))
		if err != nil {
			logging.Error(pluginCtx, "error writing response", "error", err)
		}
	})
	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		payload, err := parseWebhook(r, pluginConfig.Secret)
		if err != nil {
			logging.Error(pluginCtx, "error parsing payload", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		action := payload.Action
		jobID := stableID(payload.Job.ID)
		runID := int64(payload.Build.Number)
		if runID == 0 {
			runID = stableID(payload.Build.ID)
		}
		if jobID == 0 {
			logging.Warn(pluginCtx, "skipping buildkite webhook with empty job id")
			w.WriteHeader(http.StatusOK)
			return
		}
		status := payload.Job.State
		conclusion := buildConclusion(payload.Job.State, payload.Job.ExitStatus)
		labels := ensureSelectorLabels(payload.Job.AgentQueryRules, payload.JobEnv, payload.Job.ID)
		startedAt := parseWebhookTime(payload.Job.StartedAt)
		completedAt := parseWebhookTime(payload.Job.FinishedAt)
		simplifiedJobEvent := internalModels.QueueJob{
			Type: "BuildkiteJobPayload",
			Job: internalModels.Job{
				ID:           &jobID,
				Name:         &payload.Job.Name,
				RunID:        &runID,
				Status:       &status,
				Conclusion:   conclusion,
				StartedAt:    startedAt,
				CompletedAt:  completedAt,
				Labels:       labels,
				HTMLURL:      &payload.Job.WebURL,
				WorkflowName: &payload.Pipeline.Slug,
			},
			Action: action,
			Repository: internalModels.Repository{
				Name:  &payload.Pipeline.Slug,
				Owner: &pluginConfig.Owner,
			},
			Attempts: 0,
		}
		deliveryID := r.Header.Get("X-Buildkite-Event")
		if deliveryID == "" {
			deliveryID = payload.Event
		}

		webhookCtx := logging.AppendCtx(pluginCtx, slog.Group("job",
			slog.Group("job",
				slog.Any("labels", simplifiedJobEvent.Job.Labels),
				slog.Any("id", simplifiedJobEvent.Job.ID),
				slog.Any("name", simplifiedJobEvent.Job.Name),
				slog.Any("runID", simplifiedJobEvent.Job.RunID),
				slog.Any("htmlURL", simplifiedJobEvent.Job.HTMLURL),
				slog.Any("status", simplifiedJobEvent.Job.Status),
				slog.Any("conclusion", simplifiedJobEvent.Job.Conclusion),
				slog.Any("startedAt", simplifiedJobEvent.Job.StartedAt),
				slog.Any("completedAt", simplifiedJobEvent.Job.CompletedAt),
				slog.Any("workflowName", simplifiedJobEvent.Job.WorkflowName),
			),
			slog.String("action", simplifiedJobEvent.Action),
			slog.Any("repository", simplifiedJobEvent.Repository),
			slog.Any("ankaVM", simplifiedJobEvent.AnkaVM),
		))
		webhookCtx = logging.AppendCtx(webhookCtx, slog.String("deliveryID", deliveryID))

		logging.Info(webhookCtx, "received buildkite job event")
		if action == "queued" {
			if existsInArrayPartial(simplifiedJobEvent.Job.Labels, []string{"anka-template"}) {
				// make sure it doesn't already exist in the main queued queue
				queuedQueueName := "anklet/jobs/buildkite/queued/" + queueOwner
				inQueueJobJSON, err := queue.GetJobJSONFromQueueByID(pluginCtx, *simplifiedJobEvent.Job.ID, queuedQueueName)
				if err != nil {
					logging.Error(webhookCtx, "error searching in queue", "error", err)
					return
				}

				// Also check if it exists in any handler queues
				inHandlerQueue := false
				if inQueueJobJSON == "" && simplifiedJobEvent.Job.RunID != nil && simplifiedJobEvent.Job.ID != nil {
					inHandlerQueue, err = queue.CheckIfJobExistsInHandlerQueues(
						pluginCtx,
						*simplifiedJobEvent.Job.RunID,
						*simplifiedJobEvent.Job.ID,
						queueOwner,
						"buildkite",
					)
					if err != nil {
						logging.Error(webhookCtx, "error checking handler queues", "error", err)
						return
					}
				}

				if inQueueJobJSON == "" && !inHandlerQueue { // if it doesn't exist already
					// push it to the queue
					wrappedPayloadJSON, err := json.Marshal(simplifiedJobEvent)
					if err != nil {
						logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
						return
					}
					queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, queuedQueueName, wrappedPayloadJSON)
					if pushErr != nil {
						logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
						return
					}
					logging.Info(webhookCtx, "job pushed to queued queue", "queue", queuedQueueName, "queue_length", queueLength)
				} else {
					if inHandlerQueue {
						logging.Warn(webhookCtx, "job already being processed by a handler, rejecting duplicate queued event", "queue", queuedQueueName)
					} else {
						logging.Warn(webhookCtx, "job already present in queued queue, skipping enqueue", "queue", queuedQueueName)
					}
				}
			}
		} else if action == "in_progress" {
			if simplifiedJobEvent.Job.Conclusion != nil && *simplifiedJobEvent.Job.Conclusion == "cancelled" {
				return
			}
			// store in_progress so we can know if the registration failed
			if existsInArrayPartial(simplifiedJobEvent.Job.Labels, []string{"anka-template"}) {
				// make sure it doesn't already exist
				inProgressQueueName := "anklet/jobs/buildkite/in_progress/" + queueOwner
				inQueueJobJSON, err := queue.GetJobJSONFromQueueByID(pluginCtx, *simplifiedJobEvent.Job.ID, inProgressQueueName)
				if err != nil {
					logging.Error(webhookCtx, "error searching in queue", "error", err)
					return
				}
				if inQueueJobJSON == "" { // if it doesn't exist already
					// push it to the queue
					wrappedPayloadJSON, err := json.Marshal(simplifiedJobEvent)
					if err != nil {
						logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
						return
					}
					queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, inProgressQueueName, wrappedPayloadJSON)
					if pushErr != nil {
						logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
						return
					}
					logging.Info(webhookCtx, "job pushed to in_progress queue", "queue", inProgressQueueName, "queue_length", queueLength)
				} else {
					logging.Debug(webhookCtx, "job already present in in_progress queue, skipping enqueue", "queue", inProgressQueueName)
				}
			}
		} else if action == "completed" {
			if existsInArrayPartial(simplifiedJobEvent.Job.Labels, []string{"anka-template"}) {
				queues := []string{}
				// get all keys from database for the main queue and service queues as well as completed
				queuedKeys, err := databaseContainer.RetryKeys(pluginCtx, "anklet/jobs/buildkite/queued/"+queueOwner+"*")
				if err != nil {
					logging.Error(webhookCtx, "error getting list of queued keys (completed)", "error", err)
					return
				}
				queues = append(queues, queuedKeys...)
				results := make(chan bool, len(queues))
				var wg sync.WaitGroup
				for _, queueName := range queues {
					wg.Add(1)
					go func(queueName string) {
						defer wg.Done()
						inQueueJobJSON, err := queue.GetJobJSONFromQueueByID(pluginCtx, *simplifiedJobEvent.Job.ID, queueName)
						if err != nil {
							logging.Warn(webhookCtx, err.Error(), "queue", queueName)
						}
						results <- inQueueJobJSON != ""
					}(queueName)
				}
				go func() {
					wg.Wait()
					close(results)
				}()
				inAQueue := false
				for result := range results {
					if result {
						inAQueue = true
						break
					}
				}
				if inAQueue { // only add completed if it's in a queue
					completedQueueName := "anklet/jobs/buildkite/completed/" + queueOwner
					inCompletedQueueJobJSON, err := queue.GetJobJSONFromQueueByID(pluginCtx, *simplifiedJobEvent.Job.ID, completedQueueName)
					if err != nil {
						logging.Error(webhookCtx, "error searching in queue", "error", err)
						return
					}
					if inCompletedQueueJobJSON == "" {
						// push it to the queue
						wrappedPayloadJSON, err := json.Marshal(simplifiedJobEvent)
						if err != nil {
							logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
							return
						}
						queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, completedQueueName, wrappedPayloadJSON)
						if pushErr != nil {
							logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
							return
						}
						logging.Info(webhookCtx, "job pushed to completed queue", "queue", completedQueueName, "queue_length", queueLength)
					} else {
						logging.Debug(webhookCtx, "job already present in completed queue, skipping enqueue", "queue", completedQueueName)
					}
				}
				if !inAQueue {
					logging.Debug(webhookCtx, "job not present in any tracked queue, skipping completed enqueue",
						"job_id", simplifiedJobEvent.Job.ID,
						"queues_checked", queues,
					)
				}
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

	// var allHooks []map[string]any

	// Buildkite does not have the same hook redelivery model as GitHub.
	// We always clear in_progress on startup so handlers can recover cleanly.
	_, err = databaseContainer.RetryDel(pluginCtx, "anklet/jobs/buildkite/in_progress/"+queueOwner)
	if err != nil {
		return pluginCtx, fmt.Errorf("error deleting in_progress queue: %s", err.Error())
	}

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
	logging.Info(pluginCtx, "receiver finished starting")

	// wait for the context to be canceled
	for {
		select {
		case <-workerCtx.Done(): // use worker here, not pluginCtx
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
