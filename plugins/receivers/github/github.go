package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/internal/plugin"
)

const (
	receiverReadHeaderTimeout = 10 * time.Second
	receiverReadTimeout       = 30 * time.Second
	receiverWriteTimeout      = 30 * time.Second
	receiverIdleTimeout       = 60 * time.Second
	receiverShutdownTimeout   = 5 * time.Second
)

// Receiver implements plugin.Runner for GitHub workflow_job webhooks.
type Receiver struct {
	deps plugin.Deps
}

// NewReceiver creates a GitHub webhook receiver plugin with typed dependencies.
func NewReceiver(deps plugin.Deps) *Receiver {
	return &Receiver{deps: deps}
}

func (r *Receiver) Role() plugin.Role {
	return plugin.RoleReceiver
}

func hasAnkaTemplateLabel(labels []string) bool {
	return slices.ContainsFunc(labels, func(label string) bool {
		return strings.Contains(label, "anka-template")
	})
}

func newReceiverMux(
	pluginCtx context.Context,
	pluginConfig config.Plugin,
	databaseContainer *database.Database,
	queueOwner string,
) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logging.Debug(pluginCtx, "error writing healthcheck response", "error", err)
		}
	})
	mux.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
		deliveryID := r.Header.Get("X-GitHub-Delivery")
		decoded, rawPayload, err := decodeReceiverWebhook(r, pluginConfig.Secret)
		if err != nil {
			logging.Error(pluginCtx, "rejecting malformed webhook",
				"error", err,
				"deliveryID", deliveryID,
				"payload", truncateForLog(rawPayload),
			)
			writeMalformedWebhook(w, err)
			return
		}
		if decoded.IsWorkflowJob {
			if statusErr := handleWorkflowJobWebhook(pluginCtx, databaseContainer, queueOwner, deliveryID, decoded.Job); statusErr != nil {
				http.Error(w, "failed to process webhook", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/jobs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		if _, err := w.Write([]byte("please use /jobs/v1")); err != nil {
			logging.Debug(pluginCtx, "error writing /jobs response", "error", err)
		}
	})
	return mux
}

func handleWorkflowJobWebhook(
	pluginCtx context.Context,
	databaseContainer *database.Database,
	queueOwner string,
	deliveryID string,
	event internalGithub.QueueJob,
) error {
	webhookCtx := logging.AppendCtx(pluginCtx, slog.Group("job",
		slog.Group("workflowJob",
			slog.Any("labels", event.WorkflowJob.Labels),
			slog.Any("id", event.WorkflowJob.ID),
			slog.Any("name", event.WorkflowJob.Name),
			slog.Any("runID", event.WorkflowJob.RunID),
			slog.Any("htmlURL", event.WorkflowJob.HTMLURL),
			slog.Any("status", event.WorkflowJob.Status),
			slog.Any("conclusion", event.WorkflowJob.Conclusion),
			slog.Any("startedAt", event.WorkflowJob.StartedAt),
			slog.Any("completedAt", event.WorkflowJob.CompletedAt),
			slog.Any("workflowName", event.WorkflowJob.WorkflowName),
		),
		slog.String("action", event.Action),
		slog.Any("repository", event.Repository),
		slog.Any("ankaVM", event.AnkaVM),
	))
	webhookCtx = logging.AppendCtx(webhookCtx, slog.String("deliveryID", deliveryID))
	if event.WorkflowJob.ID != nil {
		webhookCtx = logging.AppendCtx(webhookCtx, slog.Int64("workflowJobID", *event.WorkflowJob.ID))
	}
	if event.WorkflowJob.RunID != nil {
		webhookCtx = logging.AppendCtx(webhookCtx, slog.Int64("workflowJobRunID", *event.WorkflowJob.RunID))
	}
	if event.WorkflowJob.WorkflowName != nil {
		webhookCtx = logging.AppendCtx(webhookCtx, slog.String("workflowName", *event.WorkflowJob.WorkflowName))
	}
	if event.Repository.Name != nil && event.Repository.Owner != nil {
		webhookCtx = logging.AppendCtx(webhookCtx, slog.String("repository", *event.Repository.Owner+"/"+*event.Repository.Name))
	}

	logging.Info(webhookCtx, "received workflow job to consider")
	if event.WorkflowJob.HTMLURL != nil {
		logging.Info(webhookCtx, "workflow job HTML URL", "workflowJobHTMLURL", *event.WorkflowJob.HTMLURL)
	}
	if event.WorkflowJob.Status != nil {
		logging.Info(webhookCtx, "workflow job status", "workflowJobStatus", *event.WorkflowJob.Status)
	}
	if event.WorkflowJob.Conclusion != nil {
		logging.Info(webhookCtx, "workflow job conclusion", "workflowJobConclusion", *event.WorkflowJob.Conclusion)
	}

	if !hasAnkaTemplateLabel(event.WorkflowJob.Labels) {
		return nil
	}

	switch event.Action {
	case "queued":
		return handleQueuedWebhook(pluginCtx, webhookCtx, databaseContainer, queueOwner, event)
	case "in_progress":
		return handleInProgressWebhook(pluginCtx, webhookCtx, databaseContainer, queueOwner, event)
	case "completed":
		return handleCompletedWebhook(pluginCtx, webhookCtx, databaseContainer, queueOwner, event)
	default:
		return nil
	}
}

func handleQueuedWebhook(
	pluginCtx context.Context,
	webhookCtx context.Context,
	databaseContainer *database.Database,
	queueOwner string,
	event internalGithub.QueueJob,
) error {
	queuedQueueName := "anklet/jobs/github/queued/" + queueOwner
	inQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *event.WorkflowJob.ID, queuedQueueName)
	if err != nil {
		logging.Error(webhookCtx, "error searching in queue", "error", err)
		return err
	}

	inHandlerQueue := false
	if inQueueJobJSON == "" && event.WorkflowJob.RunID != nil && event.WorkflowJob.ID != nil {
		inHandlerQueue, err = internalGithub.CheckIfJobExistsInHandlerQueues(
			pluginCtx,
			*event.WorkflowJob.RunID,
			*event.WorkflowJob.ID,
			queueOwner,
		)
		if err != nil {
			logging.Error(webhookCtx, "error checking handler queues", "error", err)
			return err
		}
	}

	if inQueueJobJSON != "" {
		logging.Warn(webhookCtx, "job already present in queued queue, skipping enqueue", "queue", queuedQueueName)
		return nil
	}
	if inHandlerQueue {
		logging.Warn(webhookCtx, "job already being processed by a handler, rejecting duplicate queued event", "queue", queuedQueueName)
		return nil
	}

	wrappedPayloadJSON, err := json.Marshal(event)
	if err != nil {
		logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
		return err
	}
	queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, queuedQueueName, wrappedPayloadJSON)
	if pushErr != nil {
		logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
		return pushErr
	}
	logging.Info(webhookCtx, "job pushed to queued queue",
		"queue", queuedQueueName,
		"queue_length", queueLength,
	)
	return nil
}

func handleInProgressWebhook(
	pluginCtx context.Context,
	webhookCtx context.Context,
	databaseContainer *database.Database,
	queueOwner string,
	event internalGithub.QueueJob,
) error {
	if event.WorkflowJob.Conclusion != nil && *event.WorkflowJob.Conclusion == "cancelled" {
		return nil
	}
	inProgressQueueName := "anklet/jobs/github/in_progress/" + queueOwner
	inQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *event.WorkflowJob.ID, inProgressQueueName)
	if err != nil {
		logging.Error(webhookCtx, "error searching in queue", "error", err)
		return err
	}
	if inQueueJobJSON != "" {
		logging.Debug(webhookCtx, "job already present in in_progress queue, skipping enqueue", "queue", inProgressQueueName)
		return nil
	}
	wrappedPayloadJSON, err := json.Marshal(event)
	if err != nil {
		logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
		return err
	}
	queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, inProgressQueueName, wrappedPayloadJSON)
	if pushErr != nil {
		logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
		return pushErr
	}
	logging.Info(webhookCtx, "job pushed to in_progress queue", "queue", inProgressQueueName, "queue_length", queueLength)
	return nil
}

func handleCompletedWebhook(
	pluginCtx context.Context,
	webhookCtx context.Context,
	databaseContainer *database.Database,
	queueOwner string,
	event internalGithub.QueueJob,
) error {
	queues, err := internalGithub.ListOwnerQueueKeys(pluginCtx, databaseContainer, "anklet/jobs/github/queued", queueOwner)
	if err != nil {
		logging.Error(webhookCtx, "error getting list of queued keys (completed)", "error", err)
		return err
	}

	results := make(chan bool, len(queues))
	var wg sync.WaitGroup
	for _, queue := range queues {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			inQueueJobJSON, lookupErr := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *event.WorkflowJob.ID, q)
			if lookupErr != nil {
				logging.Warn(webhookCtx, lookupErr.Error(), "queue", q)
			}
			results <- inQueueJobJSON != ""
		}(queue)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	inAQueue := false
	for result := range results {
		if result {
			inAQueue = true
		}
	}

	if !inAQueue {
		logging.Debug(webhookCtx, "job not present in any tracked queue, skipping completed enqueue",
			"job_id", event.WorkflowJob.ID,
			"queues_checked", queues,
		)
		return nil
	}

	completedQueueName := "anklet/jobs/github/completed/" + queueOwner
	inCompletedQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *event.WorkflowJob.ID, completedQueueName)
	if err != nil {
		logging.Error(webhookCtx, "error searching in queue", "error", err)
		return err
	}
	if inCompletedQueueJobJSON != "" {
		logging.Debug(webhookCtx, "job already present in completed queue, skipping enqueue", "queue", completedQueueName)
		return nil
	}

	wrappedPayloadJSON, err := json.Marshal(event)
	if err != nil {
		logging.Error(webhookCtx, "error converting job payload to JSON", "error", err)
		return err
	}
	queueLength, pushErr := databaseContainer.RetryRPush(pluginCtx, completedQueueName, wrappedPayloadJSON)
	if pushErr != nil {
		logging.Error(webhookCtx, "error pushing job to queue", "error", pushErr)
		return pushErr
	}
	logging.Info(webhookCtx, "job pushed to completed queue", "queue", completedQueueName, "queue_length", queueLength)
	return nil
}

// Run starts the webhook HTTP server and optional redelivery walk until workerCtx cancels.
func (r *Receiver) Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	_ context.CancelFunc,
) (context.Context, error) {
	pluginConfig := r.deps.PluginConfig
	workerGlobals := r.deps.Globals
	configFileName := r.deps.ConfigFileName
	databaseContainer := r.deps.Database
	metricsData := r.deps.Metrics

	if workerGlobals == nil {
		var err error
		workerGlobals, err = config.GetWorkerGlobalsFromContext(workerCtx)
		if err != nil {
			return pluginCtx, err
		}
	}
	if databaseContainer == nil {
		var err error
		databaseContainer, err = database.GetDatabaseFromContext(pluginCtx)
		if err != nil {
			return pluginCtx, fmt.Errorf("error getting database client from context: %w", err)
		}
	}
	if metricsData == nil {
		var err error
		metricsData, err = metrics.GetMetricsDataFromContext(workerCtx)
		if err != nil {
			return pluginCtx, err
		}
	}

	if err := pluginConfig.ValidateGitHubScope(); err != nil {
		return pluginCtx, fmt.Errorf("invalid github scope in %s:plugins:%s: %w", configFileName, pluginConfig.Name, err)
	}
	if pluginConfig.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in %s:plugins:%s<secret>", configFileName, pluginConfig.Name)
	}
	// Enterprise Cloud has no public hook-delivery REST API; redelivery is UI-only.
	if pluginConfig.IsEnterpriseScope() {
		if !pluginConfig.SkipRedeliver {
			logging.Info(pluginCtx, "enterprise scope has no hook delivery API; skipping webhook redelivery (use GitHub UI to redeliver failed deliveries)")
		}
		pluginConfig.SkipRedeliver = true
	}
	if !pluginConfig.IsEnterpriseScope() && pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in %s:plugins:%s<token/private_key>", configFileName, pluginConfig.Name)
	}
	if strings.HasPrefix(pluginConfig.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: %w", err)
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}

	var githubClient *github.Client
	var githubWrapperClient *internalGithub.GitHubClientWrapper
	if !pluginConfig.IsEnterpriseScope() {
		var err error
		githubClient, githubWrapperClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
			pluginCtx,
			pluginConfig.PrivateKey,
			pluginConfig.AppID,
			pluginConfig.InstallationID,
			pluginConfig.Token,
			pluginConfig.Enterprise,
			pluginConfig.Owner,
		)
		if err != nil {
			return pluginCtx, fmt.Errorf("error authenticating github client: %w", err)
		}
		pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	} else {
		logging.Debug(pluginCtx, "enterprise receiver skipping GitHub API authentication (webhook secret only)")
	}

	queueOwner := pluginConfig.GetQueueOwner()

	server := &http.Server{
		Addr:              ":" + pluginConfig.Port,
		Handler:           newReceiverMux(pluginCtx, pluginConfig, databaseContainer, queueOwner),
		ReadHeaderTimeout: receiverReadHeaderTimeout,
		ReadTimeout:       receiverReadTimeout,
		WriteTimeout:      receiverWriteTimeout,
		IdleTimeout:       receiverIdleTimeout,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error(pluginCtx, "receiver listener error", "error", err)
		}
	}()

	if err := metricsData.SetStatus(pluginCtx, "idle"); err != nil {
		return pluginCtx, fmt.Errorf("error updating plugin metrics: %w", err)
	}

	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Store(true)
	logging.Info(pluginCtx, "receiver finished starting")

	var redeliveryWG sync.WaitGroup
	if !pluginConfig.SkipRedeliver && githubClient != nil {
		redeliveryWG.Add(1)
		go func() {
			defer redeliveryWG.Done()
			if walkErr := runRedeliveryWalk(workerCtx, pluginCtx, githubClient, pluginConfig, queueOwner); walkErr != nil {
				if workerCtx.Err() != nil || pluginCtx.Err() != nil {
					logging.Warn(pluginCtx, "redelivery walk stopped", "error", walkErr)
					return
				}
				logging.Error(pluginCtx, "redelivery walk failed; HTTP receiver stays up", "error", walkErr)
			}
		}()
	}

	select {
	case <-workerCtx.Done():
	case <-pluginCtx.Done():
	}

	logging.Warn(pluginCtx, "shutting down receiver")
	redeliveryWG.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), receiverShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return pluginCtx, fmt.Errorf("receiver shutdown error: %w", err)
	}
	return pluginCtx, nil
}
