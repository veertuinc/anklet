package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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

// passing the the queue and ID to check for
func InQueue(serviceCtx context.Context, logger *slog.Logger, jobID int64, queue string) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
		return false, err
	}
	queued, err := databaseContainer.Client.LRange(serviceCtx, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting list of queued jobs", "err", err)
		return false, err
	}
	for _, queueItem := range queued {
		workflowJobEvent, err := database.UnwrapPayload[github.WorkflowJobEvent](queueItem)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
			return false, err
		}
		if *workflowJobEvent.WorkflowJob.ID == jobID {
			return true, fmt.Errorf("job already in queue")
		}
	}
	return false, nil
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
func Run(workerCtx context.Context, serviceCtx context.Context, serviceCancel context.CancelFunc, logger *slog.Logger, firstServiceStarted chan bool) {
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
			logger.DebugContext(serviceCtx, "received workflow job to consider",
				"workflowJob.Action", *workflowJob.Action,
				"workflowJob.WorkflowJob.Labels", workflowJob.WorkflowJob.Labels,
				"workflowJob.WorkflowJob.ID", *workflowJob.WorkflowJob.ID,
			)
			if *workflowJob.Action == "queued" {
				if exists_in_array_exact(workflowJob.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					// make sure it doesn't already exist
					inQueue, err := InQueue(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/queued")
					if err != nil {
						logger.ErrorContext(serviceCtx, "error searching in queue", "error", err)
						return
					}
					if !inQueue { // if it doesn't exist already
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

					queues := []string{}
					// get all keys from database for the main queue and service queues as well as completed
					queuedKeys, err := databaseContainer.Client.Keys(serviceCtx, "anklet/jobs/github/queued*").Result()
					if err != nil {
						logger.ErrorContext(serviceCtx, "error getting list of keys", "err", err)
						return
					}
					queues = append(queues, queuedKeys...)

					results := make(chan bool, len(queues))
					var wg sync.WaitGroup

					for _, queue := range queues {
						wg.Add(1)
						go func(queue string) {
							defer wg.Done()
							inQueue, err := InQueue(serviceCtx, logger, *workflowJob.WorkflowJob.ID, queue)
							if err != nil {
								logger.WarnContext(serviceCtx, err.Error(), "queue", queue)
							}
							results <- inQueue
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
							break
						}
					}

					if inAQueue { // only add completed if it's in a queue
						inCompletedQueue, err := InQueue(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/completed")
						if err != nil {
							logger.ErrorContext(serviceCtx, "error searching in queue", "error", err)
							return
						}
						if !inCompletedQueue {
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

					// // make sure we don't orphan completed if there is nothing in queued or other lists for it
					// inQueueQueue, err := InQueue(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/queue")
					// if err != nil {
					// 	logger.ErrorContext(serviceCtx, "error searching in queue", "error", err)
					// 	return
					// }
					// if !inQueueQueue {
					// 	// make sure it doesn't already exist
					// 	inCompletedQueue, err := InQueue(serviceCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/completed")
					// 	if err != nil {
					// 		logger.ErrorContext(serviceCtx, "error searching in queue", "error", err)
					// 		return
					// 	}
					// 	if !inCompletedQueue {
					// 		// push it to the queue
					// 		wrappedJobPayload := map[string]interface{}{
					// 			"type":    "WorkflowJobPayload",
					// 			"payload": workflowJob,
					// 		}
					// 		wrappedPayloadJSON, err := json.Marshal(wrappedJobPayload)
					// 		if err != nil {
					// 			logger.ErrorContext(serviceCtx, "error converting job payload to JSON", "error", err)
					// 			return
					// 		}
					// 		push := databaseContainer.Client.RPush(serviceCtx, "anklet/jobs/github/completed", wrappedPayloadJSON)
					// 		if push.Err() != nil {
					// 			logger.ErrorContext(serviceCtx, "error pushing job to queue", "error", push.Err())
					// 			return
					// 		}
					// 		logger.InfoContext(serviceCtx, "job pushed to completed queue", "json", string(wrappedPayloadJSON))
					// 	}
					// }
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
	// Redeliver queued jobs
	limitForHooks := time.Now().Add(-time.Hour * 24) // the time we want the stop search for redeliveries
	var allHooks []map[string]interface{}
	opts := &github.ListCursorOptions{PerPage: 10}
	doneWithHooks := false
	logger.InfoContext(serviceCtx, "listing hook deliveries to see if there any that we missed...")
	for {
		serviceCtx, hookDeliveries, response, err := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
			hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(serviceCtx, service.Owner, service.Repo, service.HookID, opts)
			if err != nil {
				return nil, nil, err
			}
			return &hookDeliveries, response, nil
		})
		if err != nil {
			logger.ErrorContext(serviceCtx, "error listing hooks", "err", err)
			return
		}
		for _, hookDelivery := range *hookDeliveries {
			// fmt.Println("hookDelivery", *hookDelivery.ID, *hookDelivery.StatusCode, *hookDelivery.Redelivery, *hookDelivery.DeliveredAt)
			if hookDelivery.StatusCode != nil && !*hookDelivery.Redelivery && *hookDelivery.Action != "in_progress" {
				serviceCtx, gottenHookDelivery, _, err := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
					gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(serviceCtx, service.Owner, service.Repo, service.HookID, *hookDelivery.ID)
					if err != nil {
						return nil, nil, err
					}
					return gottenHookDelivery, response, nil
				})
				if err != nil {
					logger.ErrorContext(serviceCtx, "error listing hooks", "err", err)
					return
				}
				var workflowJobEvent github.WorkflowJobEvent
				err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEvent)
				if err != nil {
					logger.ErrorContext(serviceCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
					return
				}
				if len(allHooks) > 0 && limitForHooks.After(gottenHookDelivery.DeliveredAt.Time) {
					doneWithHooks = true
					break
				}
				if !exists_in_array_exact(workflowJobEvent.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					continue
				}
				allHooks = append(allHooks, map[string]interface{}{
					"hookDelivery":     gottenHookDelivery,
					"workflowJobEvent": workflowJobEvent,
				})
			}
		}
		if response.Cursor == "" || doneWithHooks {
			break
		}
		opts.Cursor = response.Cursor
	}

	// get all keys from database for the main queue and service queues as well as completed
	queuedKeys, err := databaseContainer.Client.Keys(serviceCtx, "anklet/jobs/github/queued*").Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting list of keys", "err", err)
		return
	}
	var allQueuedJobs = make(map[string][]string)
	for _, key := range queuedKeys {
		queuedJobs, err := databaseContainer.Client.LRange(serviceCtx, key, 0, -1).Result()
		if err != nil {
			logger.ErrorContext(serviceCtx, "error getting list of queued jobs for key: "+key, "err", err)
			return
		}
		allQueuedJobs[key] = queuedJobs
	}
	completedKeys, err := databaseContainer.Client.Keys(serviceCtx, "anklet/jobs/github/completed*").Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting list of keys", "err", err)
		return
	}
	var allCompletedJobs = make(map[string][]string)
	for _, key := range completedKeys {
		completedJobs, err := databaseContainer.Client.LRange(serviceCtx, key, 0, -1).Result()
		if err != nil {
			logger.ErrorContext(serviceCtx, "error getting list of queued jobs for key: "+key, "err", err)
			return
		}
		allCompletedJobs[key] = completedJobs
	}

	if len(allHooks) > 0 {
	MainLoop:
		for i := len(allHooks) - 1; i >= 0; i-- { // make sure we process/redeliver queued before completed
			hookWrapper := allHooks[i]
			hookDelivery := hookWrapper["hookDelivery"].(*github.HookDelivery)
			workflowJobEvent := hookWrapper["workflowJobEvent"].(github.WorkflowJobEvent)

			inQueued := false
			// inQueuedListKey := ""
			// inQueuedListIndex := 0
			inCompleted := false
			inCompletedListKey := ""
			inCompletedIndex := 0

			// Queued deliveries
			// // always get queued jobs so that completed cleanup (when there is no queued but there is a completed) works
			for _, queuedJobs := range allQueuedJobs {
				for _, queuedJob := range queuedJobs {
					wrappedPayload, err := database.UnwrapPayload[github.WorkflowJobEvent](queuedJob)
					if err != nil {
						logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
						return
					}
					if *wrappedPayload.WorkflowJob.ID == *workflowJobEvent.WorkflowJob.ID {
						inQueued = true
						// inQueuedListKey = key
						// inQueuedListIndex = index
						break
					}
				}
			}

			// Completed deliveries
			if *hookDelivery.Action == "completed" {
				for key, completedJobs := range allCompletedJobs {
					for index, completedJob := range completedJobs {
						wrappedPayload, err := database.UnwrapPayload[github.WorkflowJobEvent](completedJob)
						if err != nil {
							logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
							return
						}
						if *wrappedPayload.WorkflowJob.ID == *workflowJobEvent.WorkflowJob.ID {
							inCompleted = true
							inCompletedListKey = key
							inCompletedIndex = index
							break
						}
					}
				}
			}

			// if in queued, but also has completed; continue and do nothing
			if inQueued && inCompleted {
				continue
			}

			// if in completed, but has no queued; remove from completed db
			if inCompleted && !inQueued {
				_, err = databaseContainer.Client.LRem(serviceCtx, inCompletedListKey, 1, allCompletedJobs[inCompletedListKey][inCompletedIndex]).Result()
				if err != nil {
					logger.ErrorContext(serviceCtx, "error removing completedJob from anklet/jobs/github/completed", "err", err, "completedJob", allCompletedJobs[inCompletedListKey][inCompletedIndex])
					return
				}
				continue
			}

			// handle queued that have already been successfully delivered before.
			if *hookDelivery.Action == "queued" {
				// check if a completed hook exists, so we don't re-queue something already finished
				for _, job := range allHooks {
					otherHookDelivery := job["hookDelivery"].(*github.HookDelivery)
					otherWorkflowJobEvent := job["workflowJobEvent"].(github.WorkflowJobEvent)
					if *otherHookDelivery.Action == "completed" && *workflowJobEvent.WorkflowJob.ID == *otherWorkflowJobEvent.WorkflowJob.ID {
						continue MainLoop
					}
				}
			}

			// if in queued, and also has a successful completed, something is wrong and we need to re-deliver it.
			if *hookDelivery.Action == "completed" && inQueued && *hookDelivery.StatusCode == 200 && !inCompleted {
				logger.InfoContext(serviceCtx, "hook delivery has completed but is still in queued; redelivering")
			} else if *hookDelivery.StatusCode == 200 || inQueued || inCompleted { // all other cases (like when it's queued); continue
				continue
			}

			// Note; We cannot (and probably should not) stop completed from being redelivered.

			// Redeliver the hook
			serviceCtx, redelivery, _, _ := ExecuteGitHubClientFunction(serviceCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Repositories.RedeliverHookDelivery(serviceCtx, service.Owner, service.Repo, service.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
			// err doesn't matter here and it will always throw "job scheduled on GitHub side; try again later"
			logger.InfoContext(serviceCtx, "hook redelivered",
				"redelivery", redelivery,
				"hookDelivery.GUID", *hookDelivery.GUID,
				"workflowJobEvent.WorkflowJob.ID", *workflowJobEvent.WorkflowJob.ID,
				"hookDelivery.Action", *hookDelivery.Action,
				"hookDelivery.StatusCode", *hookDelivery.StatusCode,
				"hookDelivery.Redelivery", *hookDelivery.Redelivery,
				"inQueued", inQueued,
				"inCompleted", inCompleted,
			)
		}
	}
	// notify the main thread that the service has started
	select {
	case <-firstServiceStarted:
	default:
		close(firstServiceStarted)
	}
	logger.InfoContext(serviceCtx, "started service")
	// wait for the context to be canceled
	<-serviceCtx.Done()
	logger.InfoContext(serviceCtx, "shutting down controller")
	if err := server.Shutdown(serviceCtx); err != nil {
		logger.ErrorContext(serviceCtx, "controller shutdown error", "error", err)
	}
}
