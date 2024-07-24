package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v63/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
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

func exists_in_array_exact(array_to_search_in []string, desired []string) bool {
	for _, desired_string := range desired {
		found := false
		for _, item := range array_to_search_in {
			if item == desired_string {
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

func EnsureNoDuplicates(serviceCtx context.Context, logger *slog.Logger, jobID int64, queue string) error {
	databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
		return err
	}
	queued, err := databaseContainer.Client.LRange(serviceCtx, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting list of queued jobs", "err", err)
		return err
	}
	for _, queueItem := range queued {
		var workflowJobEvent github.WorkflowJobEvent
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(queueItem), &payload); err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
			return err
		}
		payloadBytes, err := json.Marshal(payload["payload"])
		if err != nil {
			logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
			return err
		}
		err = json.Unmarshal(payloadBytes, &workflowJobEvent)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling payload to github.WorkflowJobEvent", "err", err)
			return err
		}
		if *workflowJobEvent.WorkflowJob.ID == jobID {
			return fmt.Errorf("job already in queue")
		}
	}
	return nil
}

// https://github.com/gofri/go-github-ratelimit has yet to support primary rate limits, so we have to do it ourselves.
func ExecuteGitHubClientFunction[T any](serviceCtx context.Context, logger *slog.Logger, executeFunc func() (*T, *github.Response, error)) (context.Context, *T, *github.Response, error) {
	result, response, err := executeFunc()
	if response != nil {
		serviceCtx = logging.AppendCtx(serviceCtx, slog.Int("api_limit_remaining", response.Rate.Remaining))
		serviceCtx = logging.AppendCtx(serviceCtx, slog.String("api_limit_reset_time", response.Rate.Reset.Time.Format(time.RFC3339)))
		serviceCtx = logging.AppendCtx(serviceCtx, slog.Int("api_limit", response.Rate.Limit))
		if response.Rate.Remaining <= 10 { // handle primary rate limiting
			sleepDuration := time.Until(response.Rate.Reset.Time) + time.Second // Adding a second to ensure we're past the reset time
			logger.WarnContext(serviceCtx, "GitHub API rate limit exceeded, sleeping until reset")
			metricsData := metrics.GetMetricsDataFromContext(serviceCtx)
			service := config.GetServiceFromContext(serviceCtx)
			metricsData.UpdateService(serviceCtx, logger, metrics.Service{
				Name:   service.Name,
				Status: "limit_paused",
			})
			select {
			case <-time.After(sleepDuration):
				metricsData.UpdateService(serviceCtx, logger, metrics.Service{
					Name:   service.Name,
					Status: "running",
				})
				return ExecuteGitHubClientFunction(serviceCtx, logger, executeFunc) // Retry the function after waiting
			case <-serviceCtx.Done():
				return serviceCtx, nil, nil, serviceCtx.Err()
			}
		}
	}
	if err != nil {
		if err.Error() != "context canceled" {
			if !strings.Contains(err.Error(), "try again later") {
				logger.Error("error executing GitHub client function: " + err.Error())
			}
		}
		return serviceCtx, nil, nil, err
	}
	return serviceCtx, result, response, nil
}

// Start runs the HTTP server
func Run(workerCtx context.Context, serviceCtx context.Context, serviceCancel context.CancelFunc, logger *slog.Logger) {
	service := config.GetServiceFromContext(serviceCtx)
	databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
		return
	}
	rateLimiter := internalGithub.GetRateLimitWaiterClientFromContext(serviceCtx)
	httpTransport := config.GetHttpTransportFromContext(serviceCtx)
	var githubClient *github.Client
	if service.PrivateKey != "" {
		itr, err := ghinstallation.NewKeyFromFile(httpTransport, int64(service.AppID), int64(service.InstallationID), service.PrivateKey)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error creating github app installation token", "err", err)
			return
		}
		rateLimiter.Transport = itr
		githubClient = github.NewClient(rateLimiter)
	} else {
		githubClient = github.NewClient(rateLimiter).WithAuthToken(service.Token)
	}

	server := &http.Server{Addr: ":" + service.Port}
	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
			return
		}
		payload, err := github.ValidatePayload(r, []byte(service.Secret))
		if err != nil {
			logger.ErrorContext(serviceCtx, "error validating payload", "error", err)
			return
		}
		event, err := github.ParseWebHook(github.WebHookType(r), payload)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error parsing event", "error", err)
			return
		}
		switch workflowJob := event.(type) {
		case *github.WorkflowJobEvent:
			// logger.DebugContext(serviceCtx, "received workflow job to consider", slog.Any("workflowJob", workflowJob))
			if *workflowJob.Action == "queued" {
				if exists_in_array_exact(workflowJob.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					// make sure it doesn't already exist
					err := EnsureNoDuplicates(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/queued")
					if err != nil {
						logger.DebugContext(serviceCtx, err.Error())
					} else {
						// push it to the queue
						wrappedJobPayload := map[string]interface{}{
							"type":    "WorkflowJobPayload",
							"payload": workflowJob,
						}
						wrappedPayloadJSON, err := json.Marshal(wrappedJobPayload)
						if err != nil {
							logger.ErrorContext(serviceCtx, "error converting job payload to JSON", "error", err)
							return
						}
						push := databaseContainer.Client.RPush(serviceCtx, "anklet/jobs/github/queued", wrappedPayloadJSON)
						if push.Err() != nil {
							logger.ErrorContext(serviceCtx, "error pushing job to queue", "error", push.Err())
							return
						}
						logger.InfoContext(serviceCtx, "job pushed to queued queue", "json", string(wrappedPayloadJSON))
					}
				}
			} else if *workflowJob.Action == "completed" {
				if exists_in_array_exact(workflowJob.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					// make sure it doesn't already exist
					err := EnsureNoDuplicates(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/completed")
					if err != nil {
						logger.DebugContext(serviceCtx, err.Error())
					} else {
						// push it to the queue
						wrappedJobPayload := map[string]interface{}{
							"type":    "WorkflowJobPayload",
							"payload": workflowJob,
						}
						wrappedPayloadJSON, err := json.Marshal(wrappedJobPayload)
						if err != nil {
							logger.ErrorContext(serviceCtx, "error converting job payload to JSON", "error", err)
							return
						}
						push := databaseContainer.Client.RPush(serviceCtx, "anklet/jobs/github/completed", wrappedPayloadJSON)
						if push.Err() != nil {
							logger.ErrorContext(serviceCtx, "error pushing job to queue", "error", push.Err())
							return
						}
						logger.InfoContext(serviceCtx, "job pushed to completed queue", "json", string(wrappedPayloadJSON))
					}
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		// w.Write([]byte("v1 jobs endpoint"))
	})
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte("please use /jobs/v1"))
	})
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.ErrorContext(serviceCtx, "controller listener error", "error", err)
		}
	}()
	// redeliver all hooks that failed while the service was down
	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	serviceCtx = context.WithValue(serviceCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	// check if the items int he queued list are even still running, otherwise remove them
	queuedJobs, err := databaseContainer.Client.LRange(serviceCtx, "anklet/jobs/github/queued", 0, -1).Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting list of queued jobs", "err", err)
		return
	}
	var firstQueuedJobStartedAtDate time.Time
	if len(queuedJobs) > 0 {
		// get the started date of the eldest item in the list of queued jobs
		var firstQueuedJobEvent github.WorkflowJobEvent
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(queuedJobs[0]), &payload); err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
			return
		}
		payloadBytes, err := json.Marshal(payload["payload"])
		if err != nil {
			logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
			return
		}
		err = json.Unmarshal(payloadBytes, &firstQueuedJobEvent)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling payload to github.WorkflowJobEvent", "err", err)
			return
		}
		if err := json.Unmarshal([]byte(queuedJobs[0]), &firstQueuedJobEvent); err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling first queued job", "err", err)
		} else {
			firstQueuedJobStartedAtDate = firstQueuedJobEvent.WorkflowJob.StartedAt.Time
		}
		var allHooks []github.HookDelivery
		opts := &github.ListCursorOptions{PerPage: 10}
		for {
			serviceCtx, hooks, response, err := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
				hooks, response, err := githubClient.Repositories.ListHookDeliveries(serviceCtx, service.Owner, service.Repo, service.HookID, opts)
				if err != nil {
					return nil, nil, err
				}
				return &hooks, response, nil
			})
			if err != nil {
				logger.ErrorContext(serviceCtx, "error listing hooks", "err", err)
				return
			}
			for _, hook := range *hooks {
				if hook.StatusCode != nil && *hook.StatusCode == 502 {
					allHooks = append(allHooks, *hook)
				}
			}
			if response.Cursor == "" || (len(allHooks) > 0 && firstQueuedJobStartedAtDate.After(allHooks[len(allHooks)-1].DeliveredAt.Time)) {
				break
			}
			opts.Cursor = response.Cursor
		}
		for _, queuedJob := range queuedJobs {
			var wrappedPayload github.WorkflowJobEvent
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(queuedJob), &payload); err != nil {
				logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
				return
			}
			payloadBytes, err := json.Marshal(payload["payload"])
			if err != nil {
				logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
				return
			}
			err = json.Unmarshal(payloadBytes, &wrappedPayload)
			if err != nil {
				logger.ErrorContext(serviceCtx, "error unmarshalling payload to github.WorkflowJobEvent", "err", err)
				return
			}
			if err := json.Unmarshal([]byte(queuedJob), &wrappedPayload); err != nil {
				logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
				return
			}
			for _, hook := range allHooks {
				serviceCtx, hookdelivery, _, err := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
					hookdelivery, response, err := githubClient.Repositories.GetHookDelivery(serviceCtx, service.Owner, service.Repo, service.HookID, *hook.ID)
					if err != nil {
						return nil, nil, err
					}
					return hookdelivery, response, nil
				})
				if err != nil {
					logger.ErrorContext(serviceCtx, "error listing hooks", "err", err)
					return
				}
				var hookResponse github.WorkflowJobEvent
				err = json.Unmarshal(*hookdelivery.Request.RawPayload, &hookResponse)
				if err != nil {
					logger.ErrorContext(serviceCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
					return
				}
				if *hookResponse.WorkflowJob.ID == *wrappedPayload.WorkflowJob.ID && !*hookdelivery.Redelivery {
					logger.DebugContext(serviceCtx, "redelivery being considered", "action", *hookResponse.Action, "workflowID", *hookResponse.WorkflowJob.ID, "redeliveryGUID", *hookdelivery.GUID)
					err := EnsureNoDuplicates(serviceCtx, logger, *hookResponse.WorkflowJob.ID, "anklet/jobs/github/"+*hookResponse.Action)
					if err != nil {
						logger.DebugContext(serviceCtx, err.Error())
					} else {
						serviceCtx, redelivery, _, _ := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
							redelivery, response, err := githubClient.Repositories.RedeliverHookDelivery(serviceCtx, service.Owner, service.Repo, service.HookID, *hookdelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return redelivery, response, nil
						})
						// err doesn't matter here and it will always throw "job scheduled on GitHub side; try again later"
						logger.InfoContext(serviceCtx, "hook redelivered", "hook", redelivery)
					}
					break
				}
			}
		}
	}
	// wait for the context to be canceled
	<-serviceCtx.Done()
	logger.InfoContext(serviceCtx, "shutting down controller")
	if err := server.Shutdown(serviceCtx); err != nil {
		logger.ErrorContext(serviceCtx, "controller shutdown error", "error", err)
	}
}
