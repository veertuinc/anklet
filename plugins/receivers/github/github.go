package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
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

func exists_in_array_partial(array_to_search_in []string, desired []string) bool {
	for _, desired_string := range desired {
		found := false
		for _, item := range array_to_search_in {
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

// passing the the queue and ID to check for
func InQueue(pluginCtx context.Context, logger *slog.Logger, jobID int64, queue string) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	queued, err := databaseContainer.Client.LRange(pluginCtx, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting list of queued jobs", "err", err)
		return false, err
	}
	for _, queueItem := range queued {
		workflowJobEvent, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](queueItem)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return false, err
		}
		if typeErr != nil { // not the type we want
			continue
		}
		if *workflowJobEvent.WorkflowJob.ID == jobID {
			// logger.WarnContext(pluginCtx, "WorkflowJob.ID already in queue", "WorkflowJob.ID", jobID)
			return true, nil
		}
	}
	return false, nil
}

// Start runs the HTTP server
func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	logger *slog.Logger,
	firstPluginStarted chan bool,
	metricsData *metrics.MetricsDataLock,
) (context.Context, error) {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	isRepoSet, err := config.GetIsRepoSetFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	metricsData.AddPlugin(
		metrics.PluginBase{
			Name:        ctxPlugin.Name,
			PluginName:  ctxPlugin.Plugin,
			RepoName:    ctxPlugin.Repo,
			OwnerName:   ctxPlugin.Owner,
			Status:      "initializing",
			StatusSince: time.Now(),
		},
	)

	configFileName, err := config.GetConfigFileNameFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	if ctxPlugin.Token == "" && ctxPlugin.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in " + configFileName + ":plugins:" + ctxPlugin.Name)
	}
	if strings.HasPrefix(ctxPlugin.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: " + err.Error())
		}
		ctxPlugin.PrivateKey = filepath.Join(homeDir, ctxPlugin.PrivateKey[2:])
	}
	if ctxPlugin.Owner == "" {
		return pluginCtx, fmt.Errorf("owner is not set in " + configFileName + ":plugins:" + ctxPlugin.Name)
	}
	if ctxPlugin.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in " + configFileName + ":plugins:" + ctxPlugin.Name)
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: " + err.Error())
	}

	var githubClient *github.Client
	githubClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		logger,
		ctxPlugin.PrivateKey,
		ctxPlugin.AppID,
		ctxPlugin.InstallationID,
		ctxPlugin.Token,
	)
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error authenticating github client", "err", err)
		return pluginCtx, fmt.Errorf("error authenticating github client: " + err.Error())
	}

	server := &http.Server{Addr: ":" + ctxPlugin.Port}
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error getting database client from context", "error", err)
			return
		}
		payload, err := github.ValidatePayload(r, []byte(ctxPlugin.Secret))
		if err != nil {
			logger.ErrorContext(pluginCtx, "error validating payload", "error", err)
			return
		}
		event, err := github.ParseWebHook(github.WebHookType(r), payload)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error parsing event", "error", err)
			return
		}
		switch workflowJob := event.(type) {
		case *github.WorkflowJobEvent:
			logger.DebugContext(pluginCtx, "received workflow job to consider",
				"workflowJob.Action", *workflowJob.Action,
				"workflowJob.WorkflowJob.Labels", workflowJob.WorkflowJob.Labels,
				"workflowJob.WorkflowJob.ID", *workflowJob.WorkflowJob.ID,
				"workflowJob.WorkflowJob.Name", *workflowJob.WorkflowJob.Name,
				"workflowJob.WorkflowJob.RunID", *workflowJob.WorkflowJob.RunID,
			)
			if *workflowJob.Action == "queued" {
				if exists_in_array_partial(workflowJob.WorkflowJob.Labels, []string{"anka-template"}) {
					// make sure it doesn't already exist
					inQueue, err := InQueue(pluginCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/queued/"+ctxPlugin.Owner)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
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
							logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
							return
						}
						push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Owner, wrappedPayloadJSON)
						if push.Err() != nil {
							logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
							return
						}
						logger.InfoContext(pluginCtx, "job pushed to queued queue",
							"workflowJob.ID", *workflowJob.WorkflowJob.ID,
							"workflowJob.Name", *workflowJob.WorkflowJob.Name,
							"workflowJob.RunID", *workflowJob.WorkflowJob.RunID,
							"html_url", *workflowJob.WorkflowJob.HTMLURL,
							"status", *workflowJob.WorkflowJob.Status,
							"conclusion", workflowJob.WorkflowJob.Conclusion,
							"started_at", workflowJob.WorkflowJob.StartedAt,
							"completed_at", workflowJob.WorkflowJob.CompletedAt,
						)
					}
				}
			} else if *workflowJob.Action == "completed" {
				if exists_in_array_partial(workflowJob.WorkflowJob.Labels, []string{"anka-template"}) {

					queues := []string{}
					// get all keys from database for the main queue and service queues as well as completed
					queuedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Owner+"*").Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error getting list of keys", "err", err)
						return
					}
					queues = append(queues, queuedKeys...)
					results := make(chan bool, len(queues))
					var wg sync.WaitGroup
					for _, queue := range queues {
						wg.Add(1)
						go func(queue string) {
							defer wg.Done()
							inQueue, err := InQueue(pluginCtx, logger, *workflowJob.WorkflowJob.ID, queue)
							if err != nil {
								logger.WarnContext(pluginCtx, err.Error(), "queue", queue)
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
						inCompletedQueue, err := InQueue(pluginCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/completed/"+ctxPlugin.Owner)
						if err != nil {
							logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
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
								logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
								return
							}
							push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/completed/"+ctxPlugin.Owner, wrappedPayloadJSON)
							if push.Err() != nil {
								logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
								return
							}
							logger.InfoContext(pluginCtx, "job pushed to completed queue",
								"workflowJob.ID", *workflowJob.WorkflowJob.ID,
								"workflowJob.Name", *workflowJob.WorkflowJob.Name,
								"workflowJob.RunID", *workflowJob.WorkflowJob.RunID,
								"html_url", *workflowJob.WorkflowJob.HTMLURL,
								"status", *workflowJob.WorkflowJob.Status,
								"conclusion", workflowJob.WorkflowJob.Conclusion,
								"started_at", workflowJob.WorkflowJob.StartedAt,
								"completed_at", workflowJob.WorkflowJob.CompletedAt,
							)
						}
					}

					// // make sure we don't orphan completed if there is nothing in queued or other lists for it
					// inQueueQueue, err := InQueue(pluginCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/queue")
					// if err != nil {
					// 	logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
					// 	return
					// }
					// if !inQueueQueue {
					// 	// make sure it doesn't already exist
					// 	inCompletedQueue, err := InQueue(pluginCtx, logger, *workflowJob.WorkflowJob.ID, "anklet/jobs/github/completed")
					// 	if err != nil {
					// 		logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
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
					// 			logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
					// 			return
					// 		}
					// 		push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/completed", wrappedPayloadJSON)
					// 		if push.Err() != nil {
					// 			logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
					// 			return
					// 		}
					// 		logger.InfoContext(pluginCtx, "job pushed to completed queue", "json", string(wrappedPayloadJSON))
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
			logger.ErrorContext(pluginCtx, "receiver listener error", "error", err)
		}
	}()

	// var allHooks []map[string]interface{}

	/// OLD STUFF

	// if !ctxPlugin.SkipRedeliver {
	// 	// Redeliver queued jobs
	// 	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	// 	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	// 	limitForHooks := time.Now().Add(-time.Hour * time.Duration(ctxPlugin.RedeliverHours)) // the time we want the stop search for redeliveries
	// 	opts := &github.ListCursorOptions{PerPage: 10}
	// 	logger.InfoContext(pluginCtx, fmt.Sprintf("listing hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", ctxPlugin.RedeliverHours))
	// 	reachedLimitTime := false

	// 	for {
	// 		if reachedLimitTime {
	// 			break
	// 		}
	// 		var hookDeliveries *[]*github.HookDelivery
	// 		var response *github.Response
	// 		var err error
	// 		if isRepoSet {
	// 			pluginCtx, hookDeliveries, response, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
	// 				hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, opts)
	// 				if err != nil {
	// 					return nil, nil, err
	// 				}
	// 				return &hookDeliveries, response, nil
	// 			})
	// 		} else {
	// 			pluginCtx, hookDeliveries, response, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
	// 				hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, opts)
	// 				if err != nil {
	// 					return nil, nil, err
	// 				}
	// 				return &hookDeliveries, response, nil
	// 			})
	// 		}
	// 		if err != nil {
	// 			logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
	// 			return
	// 		}

	// 		// // Filter out any objects in hookDeliveries that have status_code != 200
	// 		// var failedHookDeliveries []*github.HookDelivery
	// 		// for _, hookDelivery := range *hookDeliveries {
	// 		// 	if hookDelivery.StatusCode != nil && *hookDelivery.StatusCode != 200 {
	// 		// 		failedHookDeliveries = append(failedHookDeliveries, hookDelivery)
	// 		// 	}
	// 		// }

	// 		// // Pretty print hookDeliveries
	// 		// hookDeliveriesJSON, err := json.MarshalIndent(failedHookDeliveries, "", "  ")
	// 		// if err != nil {
	// 		// 	logger.ErrorContext(pluginCtx, "error marshalling hook deliveries to JSON", "err", err)
	// 		// 	return
	// 		// }
	// 		// fmt.Println("Hook Deliveries:", string(hookDeliveriesJSON))

	// 		for _, hookDelivery := range *hookDeliveries {
	// 			if hookDelivery.Action == nil { // this prevents webhooks like the ping which might be in the list from causing errors since they dont have an action
	// 				continue
	// 			}
	// 			fmt.Println("hookDelivery", hookDelivery)
	// 			if limitForHooks.After(hookDelivery.DeliveredAt.Time) {
	// 				fmt.Println("reached end of time")
	// 				reachedLimitTime = true
	// 				break
	// 			}
	// 			if hookDelivery.StatusCode != nil && !*hookDelivery.Redelivery && *hookDelivery.Action != "in_progress" {
	// 				var gottenHookDelivery *github.HookDelivery
	// 				var err error
	// 				if isRepoSet {
	// 					pluginCtx, gottenHookDelivery, _, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
	// 						gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, *hookDelivery.ID)
	// 						if err != nil {
	// 							return nil, nil, err
	// 						}
	// 						return gottenHookDelivery, response, nil
	// 					})
	// 				} else {
	// 					pluginCtx, gottenHookDelivery, _, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
	// 						gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, *hookDelivery.ID)
	// 						if err != nil {
	// 							return nil, nil, err
	// 						}
	// 						return gottenHookDelivery, response, nil
	// 					})
	// 				}
	// 				if err != nil {
	// 					logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
	// 					return
	// 				}
	// 				var workflowJobEvent github.WorkflowJobEvent
	// 				err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEvent)
	// 				if err != nil {
	// 					logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
	// 					return
	// 				}
	// 				if !exists_in_array_partial(workflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
	// 					continue
	// 				}
	// 				allHooks = append(allHooks, map[string]interface{}{
	// 					"hookDelivery":     gottenHookDelivery,
	// 					"workflowJobEvent": workflowJobEvent,
	// 				})
	// 			}
	// 		}
	// 		if response.Cursor == "" {
	// 			break
	// 		}
	// 		opts.Cursor = response.Cursor
	// 	}
	// }

	var hookDeliveries *[]*github.HookDelivery
	toRedeliver := []*github.HookDelivery{}

	if !ctxPlugin.SkipRedeliver {
		// Redeliver queued jobs
		githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
		pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
		limitForHooks := time.Now().Add(-time.Hour * time.Duration(ctxPlugin.RedeliverHours)) // the time we want the stop search for redeliveries
		opts := &github.ListCursorOptions{PerPage: 10}
		logger.InfoContext(pluginCtx, fmt.Sprintf("listing hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", ctxPlugin.RedeliverHours))
		// var response *github.Response
		var err error
		if isRepoSet {
			pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
				hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, opts)
				if err != nil {
					return nil, nil, err
				}
				return &hookDeliveries, response, nil
			})
		} else {
			pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
				hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, opts)
				if err != nil {
					return nil, nil, err
				}
				return &hookDeliveries, response, nil
			})
		}
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
			return pluginCtx, fmt.Errorf("error listing hooks: %s", err.Error())
		}

		for _, hookDelivery := range *hookDeliveries {
			if hookDelivery.Action == nil { // this prevents webhooks like the ping which might be in the list from causing errors since they dont have an action
				continue
			}
			if limitForHooks.After(hookDelivery.DeliveredAt.Time) {
				// fmt.Println("reached end of time")
				break
			}
			if hookDelivery.StatusCode != nil && *hookDelivery.StatusCode != 200 && !*hookDelivery.Redelivery && *hookDelivery.Action != "in_progress" {
				// fmt.Println("NEED: ", hookDelivery)
				var found *github.HookDelivery
				for _, otherHookDelivery := range *hookDeliveries {
					if hookDelivery.ID != nil && otherHookDelivery.ID != nil && *hookDelivery.ID != *otherHookDelivery.ID &&
						otherHookDelivery.GUID != nil && hookDelivery.GUID != nil && *otherHookDelivery.GUID == *hookDelivery.GUID &&
						otherHookDelivery.Redelivery != nil && *otherHookDelivery.Redelivery &&
						otherHookDelivery.StatusCode != nil && *otherHookDelivery.StatusCode == 200 &&
						otherHookDelivery.DeliveredAt.Time.After(hookDelivery.DeliveredAt.Time) {
						found = otherHookDelivery
						break
					}
				}
				if found != nil {
					// fmt.Println("FOUND :", found)
					continue
				} else {
					// schedule for redelivery
					toRedeliver = append(toRedeliver, hookDelivery)
					// fmt.Println("NOT FOUND: ", hookDelivery)
				}
			}
		}
	}

	// if len(allHooks) > 0 {
	// 	// get all keys from database for the main queue and service queues as well as completed
	queuedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Owner+"*").Result()
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error getting list of keys", "err", err)
		return pluginCtx, fmt.Errorf("error getting list of keys: %s", err.Error())
	}
	var allQueuedJobs = make(map[string][]string)
	for _, key := range queuedKeys {
		queuedJobs, err := databaseContainer.Client.LRange(pluginCtx, key, 0, -1).Result()
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error getting list of queued jobs for key: "+key, "err", err)
			return pluginCtx, fmt.Errorf("error getting list of queued jobs for key: %s", err.Error())
		}
		allQueuedJobs[key] = queuedJobs
	}
	completedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/completed"+ctxPlugin.Owner+"*").Result()
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error getting list of keys", "err", err)
		return pluginCtx, fmt.Errorf("error getting list of keys: %s", err.Error())
	}
	var allCompletedJobs = make(map[string][]string)
	for _, key := range completedKeys {
		completedJobs, err := databaseContainer.Client.LRange(pluginCtx, key, 0, -1).Result()
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error getting list of queued jobs for key: "+key, "err", err)
			return pluginCtx, fmt.Errorf("error getting list of queued jobs for key: %s", err.Error())
		}
		allCompletedJobs[key] = completedJobs
	}

MainLoop:
	for i := len(toRedeliver) - 1; i >= 0; i-- { // make sure we process/redeliver queued before completed
		hookDelivery := toRedeliver[i]

		var gottenHookDelivery *github.HookDelivery
		var err error
		if isRepoSet {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		} else {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		}
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
			return pluginCtx, fmt.Errorf("error listing hooks: %s", err.Error())
		}
		var workflowJobEvent github.WorkflowJobEvent
		err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEvent)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
			return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
		}

		inQueued := false
		// inQueuedListKey := ""
		// inQueuedListIndex := 0
		inCompleted := false
		inCompletedListKey := ""
		inCompletedIndex := 0

		// logger.DebugContext(pluginCtx, "hookDelivery", "hookDelivery", hookDelivery)
		// logger.DebugContext(pluginCtx, "workflowJobEvent", "workflowJobEvent", workflowJobEvent)

		// Check if the job is already in the queue
		if workflowJobEvent.WorkflowJob == nil || workflowJobEvent.WorkflowJob.ID == nil {
			logger.WarnContext(pluginCtx, "WorkflowJob or WorkflowJob.ID is nil")
			continue
		}

		// Queued deliveries
		// // always get queued jobs so that completed cleanup (when there is no queued but there is a completed) works
		for _, queuedJobs := range allQueuedJobs {
			for _, queuedJob := range queuedJobs {
				if queuedJob == "" {
					continue
				}
				wrappedPayload, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](queuedJob)
				if err != nil {
					// logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
					return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
				}
				if typeErr != nil { // not the type we want
					continue
				}
				if wrappedPayload.WorkflowJob.ID == nil {
					continue
				}
				if workflowJobEvent.WorkflowJob.ID == nil {
					continue
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
					wrappedPayload, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](completedJob)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
						return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
					}
					if typeErr != nil { // not the type we want
						continue
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
			_, err = databaseContainer.Client.LRem(pluginCtx, inCompletedListKey, 1, allCompletedJobs[inCompletedListKey][inCompletedIndex]).Result()
			if err != nil {
				// logger.ErrorContext(pluginCtx, "error removing completedJob from anklet/jobs/github/completed/"+ctxPlugin.Owner, "err", err, "completedJob", allCompletedJobs[inCompletedListKey][inCompletedIndex])
				return pluginCtx, fmt.Errorf("error removing completedJob from anklet/jobs/github/completed/"+ctxPlugin.Owner+": %s", err.Error())
			}
			continue
		}

		// handle queued that have already been successfully delivered before.
		if *hookDelivery.Action == "queued" {
			// check if a completed hook exists, so we don't re-queue something already finished
			for _, otherHookDelivery := range *hookDeliveries {
				if *otherHookDelivery.Action == "completed" &&
					otherHookDelivery.DeliveredAt != nil && otherHookDelivery.DeliveredAt.Time.After(hookDelivery.DeliveredAt.Time) &&
					otherHookDelivery.RepositoryID != nil && *otherHookDelivery.RepositoryID == *hookDelivery.RepositoryID {
					var otherGottenHookDelivery *github.HookDelivery
					var err error
					if isRepoSet {
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, *hookDelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return otherGottenHookDelivery, response, nil
						})
					} else {
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, *hookDelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return otherGottenHookDelivery, response, nil
						})
					}
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
						return pluginCtx, fmt.Errorf("error listing hooks: %s", err.Error())
					}
					var otherWorkflowJobEvent github.WorkflowJobEvent
					err = json.Unmarshal(*otherGottenHookDelivery.Request.RawPayload, &otherWorkflowJobEvent)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
						return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
					}
					if *workflowJobEvent.WorkflowJob.ID == *otherWorkflowJobEvent.WorkflowJob.ID {
						continue MainLoop
					}
				}
			}
		}

		// if in queued, and also has a successful completed, something is wrong and we need to re-deliver it.
		if *hookDelivery.Action == "completed" && inQueued && *hookDelivery.StatusCode == 200 && !inCompleted {
			logger.InfoContext(pluginCtx, "hook delivery has completed but is still in queued; redelivering")
		} else if *hookDelivery.StatusCode == 200 || inCompleted { // all other cases (like when it's queued); continue
			continue
		}

		// Note; We cannot (and probably should not) stop completed from being redelivered.

		// Redeliver the hook
		var redelivery *github.HookDelivery
		if isRepoSet {
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Repositories.RedeliverHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.Repo, ctxPlugin.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
		} else {
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Organizations.RedeliverHookDelivery(pluginCtx, ctxPlugin.Owner, ctxPlugin.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
		}
		// err doesn't matter here and it will always throw "job scheduled on GitHub side; try again later"
		logger.InfoContext(pluginCtx, "hook redelivered",
			"redelivery", redelivery,
			"hookDelivery.GUID", *hookDelivery.GUID,
			"workflowJobEvent.WorkflowJob.ID", *workflowJobEvent.WorkflowJob.ID,
			"hookDelivery.Action", *hookDelivery.Action,
			"hookDelivery.StatusCode", *hookDelivery.StatusCode,
			"hookDelivery.Redelivery", *hookDelivery.Redelivery,
			"inCompleted", inCompleted,
		)
	}

	// notify the main thread that the service has started
	select {
	case <-firstPluginStarted:
	default:
		close(firstPluginStarted)
	}
	logger.InfoContext(pluginCtx, "started plugin")
	metrics.UpdatePlugin(workerCtx, pluginCtx, logger, metrics.PluginBase{
		Status: "running",
	})
	// wait for the context to be canceled
	<-pluginCtx.Done()
	logger.InfoContext(pluginCtx, "shutting down receiver")
	if err := server.Shutdown(pluginCtx); err != nil {
		// logger.ErrorContext(pluginCtx, "receiver shutdown error", "error", err)
		return pluginCtx, fmt.Errorf("receiver shutdown error: %s", err.Error())
	}
	return pluginCtx, nil
}
