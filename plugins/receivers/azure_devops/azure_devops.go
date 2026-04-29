package azure_devops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/veertuinc/anklet/internal/azure_devops"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/jobqueue"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

// Server defines the structure for the API server.
type Server struct {
	Port string
}

// NewServer creates a new instance of Server.
func NewServer(port string) *Server {
	return &Server{Port: port}
}

// queueJobByID looks up a queued azure_devops.QueueJob in queueName by its
// upstream Job.ID. Returns ("", nil) when not found.
func queueJobByID(ctx context.Context, jobID, queueName string) (string, error) {
	return jobqueue.GetJobJSONByID[azure_devops.QueueJob, string](
		ctx, queueName, jobID,
		func(q azure_devops.QueueJob) string { return q.Job.ID },
	)
}

// Run starts the HTTP server for receiving Azure DevOps webhooks.
func Run(workerCtx context.Context, pluginCtx context.Context) (context.Context, error) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}

	if err := pluginConfig.ValidateAzureDevOps(); err != nil {
		return pluginCtx, err
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: %w", err)
	}

	queueNS := pluginConfig.AzureDevOpsRedisNamespace()
	queuedQueueName := "anklet/jobs/azure_devops/queued/" + queueNS
	inProgressQueueName := "anklet/jobs/azure_devops/in_progress/" + queueNS
	completedQueueName := "anklet/jobs/azure_devops/completed/" + queueNS

	// In-progress queue is recovery state; reset it on startup so a previously
	// crashed receiver does not leak phantom in-flight entries.
	if _, err := databaseContainer.RetryDel(pluginCtx, inProgressQueueName); err != nil {
		return pluginCtx, fmt.Errorf("error deleting in_progress queue: %w", err)
	}

	// Confirm credentials and project access before we accept any webhooks.
	adoClient, err := azure_devops.NewClient(pluginCtx, pluginConfig.OrganizationURL, pluginConfig.Project, pluginConfig.Token)
	if err != nil {
		return pluginCtx, fmt.Errorf("error creating azure devops client: %w", err)
	}
	if err := adoClient.Ping(pluginCtx); err != nil {
		return pluginCtx, fmt.Errorf("error pinging azure devops API: %w", err)
	}
	logging.Info(pluginCtx, "azure devops API reachable", "organization_url", pluginConfig.OrganizationURL, "project", pluginConfig.Project)

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:              ":" + pluginConfig.Port,
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}

	mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logging.Error(pluginCtx, "error writing response", "error", err)
		}
	})

	mux.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(pluginCtx, w, r, queueNS, queuedQueueName, inProgressQueueName, completedQueueName)
	})

	mux.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		if _, err := w.Write([]byte("please use /jobs/v1")); err != nil {
			logging.Error(pluginCtx, "error writing response", "error", err)
		}
	})

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Error(pluginCtx, "receiver listener error", "error", err)
		}
	}()

	if err := metrics.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
		Status:      "running",
		StatusSince: time.Now(),
	}); err != nil {
		return pluginCtx, fmt.Errorf("error updating plugin metrics: %w", err)
	}

	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Store(true)
	logging.Info(pluginCtx, "azure devops receiver finished starting")

	for {
		select {
		case <-workerCtx.Done():
			logging.Warn(pluginCtx, "shutting down receiver")
			if err := server.Shutdown(pluginCtx); err != nil {
				return pluginCtx, fmt.Errorf("receiver shutdown error: %w", err)
			}
			return pluginCtx, nil
		case <-time.After(time.Second):
			continue
		}
	}
}

// handleWebhook processes a single Azure DevOps webhook delivery. Filters to
// `ms.vss-pipelines.job-state-changed-event`, normalizes the payload into an
// azure_devops.QueueJob, and routes it to the queued / in_progress / completed
// Redis lists based on the job's reported state.
//
// TODO(open-question): webhook signature validation. Azure DevOps service
// hooks support a basic-auth username/password mode; once we decide on that,
// add a check at the top of this handler.
func handleWebhook(
	pluginCtx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	queueNS, queuedQueueName, inProgressQueueName, completedQueueName string,
) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
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

	var hook azure_devops.WebhookPayload
	if err := json.Unmarshal(body, &hook); err != nil {
		logging.Error(pluginCtx, "error parsing webhook payload", "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if hook.EventType != azure_devops.JobStateChangedEvent {
		logging.Debug(pluginCtx, "ignoring non-job-state-changed event", "eventType", hook.EventType)
		w.WriteHeader(http.StatusOK)
		return
	}

	queueJob := azure_devops.QueueJob{
		BaseQueueJob: jobqueue.BaseQueueJob{
			Type:   "AzureDevOpsJob",
			Action: azure_devops.MapJobStateToAction(hook.Resource.Job.State, hook.Resource.Job.Result),
		},
		Job:       hook.Resource.Job,
		Run:       hook.Resource.Run,
		Pipeline:  hook.Resource.Pipeline,
		ProjectID: hook.Resource.ProjectID,
		OrgURL:    hook.ResourceContainers.Account.BaseURL,
	}

	webhookCtx := logging.AppendCtx(pluginCtx, slog.Group("job",
		slog.String("jobID", queueJob.Job.ID),
		slog.String("jobName", queueJob.Job.Name),
		slog.String("jobState", queueJob.Job.State),
		slog.String("jobResult", queueJob.Job.Result),
		slog.Int("runID", queueJob.Run.ID),
		slog.String("runName", queueJob.Run.Name),
		slog.String("pipelineName", queueJob.Pipeline.Name),
		slog.String("projectID", queueJob.ProjectID),
		slog.String("action", queueJob.Action),
	))
	webhookCtx = logging.AppendCtx(webhookCtx, slog.String("notificationID", fmt.Sprintf("%d", hook.NotificationID)))

	logging.Info(webhookCtx, "received azure devops job webhook")

	switch hook.Resource.Job.State {
	case azure_devops.JobStateWaiting:
		if err := pushIfAbsent(webhookCtx, queueJob, queuedQueueName, "queued"); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	case azure_devops.JobStateInProgress:
		if err := pushIfAbsent(webhookCtx, queueJob, inProgressQueueName, "in_progress"); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	case azure_devops.JobStateCompleted:
		if err := pushCompletedIfTracked(webhookCtx, queueJob, queueNS, completedQueueName); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	default:
		logging.Debug(webhookCtx, "ignoring job state", "state", hook.Resource.Job.State)
	}

	w.WriteHeader(http.StatusOK)
}

// pushIfAbsent pushes job onto queueName as JSON when no entry with the same
// Job.ID already lives there. Used to deduplicate webhook redeliveries.
func pushIfAbsent(ctx context.Context, job azure_devops.QueueJob, queueName, label string) error {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		logging.Error(ctx, "error getting database client", "error", err)
		return err
	}
	existing, err := queueJobByID(ctx, job.Job.ID, queueName)
	if err != nil {
		logging.Error(ctx, "error searching in queue", "error", err, "queue", queueName)
		return err
	}
	if existing != "" {
		logging.Debug(ctx, "job already present in queue, skipping enqueue", "queue", queueName, "label", label)
		return nil
	}
	payload, err := json.Marshal(job)
	if err != nil {
		logging.Error(ctx, "error converting job payload to JSON", "error", err)
		return err
	}
	queueLength, err := databaseContainer.RetryRPush(ctx, queueName, payload)
	if err != nil {
		logging.Error(ctx, "error pushing job to queue", "error", err, "queue", queueName)
		return err
	}
	logging.Info(ctx, "job pushed to "+label+" queue", "queue", queueName, "queue_length", queueLength)
	return nil
}

// pushCompletedIfTracked pushes job onto completedQueueName only if the job
// was previously seen in any queued queue. Mirrors the legacy GitHub
// receiver's policy of not retaining completion records for jobs that no
// handler ever picked up.
func pushCompletedIfTracked(ctx context.Context, job azure_devops.QueueJob, queueNS, completedQueueName string) error {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		logging.Error(ctx, "error getting database client", "error", err)
		return err
	}
	queuedKeys, err := databaseContainer.RetryKeys(ctx, "anklet/jobs/azure_devops/queued/"+queueNS+"*")
	if err != nil {
		logging.Error(ctx, "error getting list of queued keys", "error", err)
		return err
	}
	tracked := false
	for _, queue := range queuedKeys {
		hit, err := queueJobByID(ctx, job.Job.ID, queue)
		if err != nil {
			logging.Warn(ctx, "error searching in queue", "error", err, "queue", queue)
			continue
		}
		if hit != "" {
			tracked = true
			break
		}
	}
	if !tracked {
		logging.Debug(ctx, "job not present in any tracked queue, skipping completed enqueue",
			"job_id", job.Job.ID,
			"queues_checked", queuedKeys,
		)
		return nil
	}
	return pushIfAbsent(ctx, job, completedQueueName, "completed")
}
