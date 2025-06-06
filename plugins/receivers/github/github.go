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
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/metrics"
)

// Server defines the structure for the API server
type Server struct {
	Port string
}

var once sync.Once

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

// Start runs the HTTP server
func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	logger *slog.Logger,
	metricsData *metrics.MetricsDataLock,
) (context.Context, error) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}
	isRepoSet, err := config.GetIsRepoSetFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	metricsData.AddPlugin(
		metrics.PluginBase{
			Name:        pluginConfig.Name,
			PluginName:  pluginConfig.Plugin,
			RepoName:    pluginConfig.Repo,
			OwnerName:   pluginConfig.Owner,
			Status:      "initializing",
			StatusSince: time.Now(),
		},
	)

	once.Do(func() {
		metrics.ExportMetricsToDB(pluginCtx, logger)
	})
	configFileName, err := config.GetConfigFileNameFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	if pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in " + configFileName + ":plugins:" + pluginConfig.Name)
	}
	if strings.HasPrefix(pluginConfig.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: " + err.Error())
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}
	if pluginConfig.Owner == "" {
		return pluginCtx, fmt.Errorf("owner is not set in " + configFileName + ":plugins:" + pluginConfig.Name)
	}
	if pluginConfig.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in " + configFileName + ":plugins:" + pluginConfig.Name)
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: " + err.Error())
	}

	var githubClient *github.Client
	githubClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		logger,
		pluginConfig.PrivateKey,
		pluginConfig.AppID,
		pluginConfig.InstallationID,
		pluginConfig.Token,
	)
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error authenticating github client", "err", err)
		return pluginCtx, fmt.Errorf("error authenticating github client: " + err.Error())
	}

	// clean up in_progress queue if it exists
	err = databaseContainer.Client.Del(pluginCtx, "anklet/jobs/github/in_progress/"+pluginConfig.Owner).Err()
	if err != nil {
		return pluginCtx, fmt.Errorf("error deleting in_progress queue: " + err.Error())
	}

	server := &http.Server{Addr: ":" + pluginConfig.Port}
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			logger.ErrorContext(pluginCtx, "error getting database client from context", "error", err)
			return
		}
		payload, err := github.ValidatePayload(r, []byte(pluginConfig.Secret))
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
			simplifiedWorkflowJobEvent := internalGithub.QueueJob{
				Type: "WorkflowJobPayload",
				WorkflowJob: internalGithub.SimplifiedWorkflowJob{
					ID:           workflowJob.WorkflowJob.ID,
					Name:         workflowJob.WorkflowJob.Name,
					RunID:        workflowJob.WorkflowJob.RunID,
					Status:       workflowJob.WorkflowJob.Status,
					Conclusion:   workflowJob.WorkflowJob.Conclusion,
					StartedAt:    workflowJob.WorkflowJob.StartedAt,
					CompletedAt:  workflowJob.WorkflowJob.CompletedAt,
					Labels:       workflowJob.WorkflowJob.Labels,
					HTMLURL:      workflowJob.WorkflowJob.HTMLURL,
					WorkflowName: workflowJob.WorkflowJob.WorkflowName,
				},
				Action: *workflowJob.Action,
				Repository: internalGithub.Repository{
					Name: workflowJob.Repo.Name,
				},
				AnkaVM: anka.VM{},
			}
			logger.InfoContext(pluginCtx, "received workflow job to consider",
				"workflowJob.WorkflowJob.Labels", simplifiedWorkflowJobEvent.WorkflowJob.Labels,
				"workflowJob.WorkflowJob.ID", simplifiedWorkflowJobEvent.WorkflowJob.ID,
				"workflowJob.WorkflowJob.Name", simplifiedWorkflowJobEvent.WorkflowJob.Name,
				"workflowJob.WorkflowJob.RunID", simplifiedWorkflowJobEvent.WorkflowJob.RunID,
				"workflowJob.WorkflowJob.HTMLURL", simplifiedWorkflowJobEvent.WorkflowJob.HTMLURL,
				"workflowJob.WorkflowJob.Status", simplifiedWorkflowJobEvent.WorkflowJob.Status,
				"workflowJob.WorkflowJob.Conclusion", simplifiedWorkflowJobEvent.WorkflowJob.Conclusion,
				"workflowJob.WorkflowJob.StartedAt", simplifiedWorkflowJobEvent.WorkflowJob.StartedAt,
				"workflowJob.WorkflowJob.CompletedAt", simplifiedWorkflowJobEvent.WorkflowJob.CompletedAt,
				"workflowJob.Action", simplifiedWorkflowJobEvent.Action,
				"workflowJob.WorkflowJob.WorkflowName", simplifiedWorkflowJobEvent.WorkflowJob.WorkflowName,
				"workflowJob.Repository.Name", simplifiedWorkflowJobEvent.Repository.Name,
				"workflowJob.AnkaVM", simplifiedWorkflowJobEvent.AnkaVM,
			)
			if *workflowJob.Action == "queued" {
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					// make sure it doesn't already exist
					inQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, "anklet/jobs/github/queued/"+pluginConfig.Owner)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
						return
					}
					if inQueueJobJSON == "" { // if it doesn't exist already
						// push it to the queue
						wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
						if err != nil {
							logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
							return
						}
						push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner, wrappedPayloadJSON)
						if push.Err() != nil {
							logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
							return
						}
						logger.InfoContext(pluginCtx, "job pushed to queued queue",
							"workflowJob.ID", simplifiedWorkflowJobEvent.WorkflowJob.ID,
							"workflowJob.Name", simplifiedWorkflowJobEvent.WorkflowJob.Name,
							"workflowJob.RunID", simplifiedWorkflowJobEvent.WorkflowJob.RunID,
							"html_url", simplifiedWorkflowJobEvent.WorkflowJob.HTMLURL,
							"status", simplifiedWorkflowJobEvent.WorkflowJob.Status,
							"conclusion", simplifiedWorkflowJobEvent.WorkflowJob.Conclusion,
							"started_at", simplifiedWorkflowJobEvent.WorkflowJob.StartedAt,
							"completed_at", simplifiedWorkflowJobEvent.WorkflowJob.CompletedAt,
							"repository", simplifiedWorkflowJobEvent.Repository,
							"action", simplifiedWorkflowJobEvent.Action,
							"anka_vm", simplifiedWorkflowJobEvent.AnkaVM,
						)
					}
				}
			} else if *workflowJob.Action == "in_progress" {
				if workflowJob.WorkflowJob.Conclusion != nil && *workflowJob.WorkflowJob.Conclusion == "cancelled" {
					return
				}
				// store in_progress so we can know if the registration failed
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					// make sure it doesn't already exist
					inQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, "anklet/jobs/github/in_progress/"+pluginConfig.Owner)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
						return
					}
					if inQueueJobJSON == "" { // if it doesn't exist already
						// push it to the queue
						wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
						if err != nil {
							logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
							return
						}
						push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/in_progress/"+pluginConfig.Owner, wrappedPayloadJSON)
						if push.Err() != nil {
							logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
							return
						}
						logger.InfoContext(pluginCtx, "job pushed to in_progress queue",
							"workflowJob.ID", simplifiedWorkflowJobEvent.WorkflowJob.ID,
							"workflowJob.Name", simplifiedWorkflowJobEvent.WorkflowJob.Name,
							"workflowJob.RunID", simplifiedWorkflowJobEvent.WorkflowJob.RunID,
							"html_url", simplifiedWorkflowJobEvent.WorkflowJob.HTMLURL,
							"status", simplifiedWorkflowJobEvent.WorkflowJob.Status,
							"conclusion", simplifiedWorkflowJobEvent.WorkflowJob.Conclusion,
							"started_at", simplifiedWorkflowJobEvent.WorkflowJob.StartedAt,
							"completed_at", simplifiedWorkflowJobEvent.WorkflowJob.CompletedAt,
						)
					}
				}
			} else if *workflowJob.Action == "completed" {
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					queues := []string{}
					// get all keys from database for the main queue and service queues as well as completed
					queuedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"*").Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error getting list of queued keys (completed)", "err", err)
						return
					}
					queues = append(queues, queuedKeys...)
					results := make(chan bool, len(queues))
					var wg sync.WaitGroup
					for _, queue := range queues {
						wg.Add(1)
						go func(queue string) {
							defer wg.Done()
							inQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, queue)
							if err != nil {
								logger.WarnContext(pluginCtx, err.Error(), "queue", queue)
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
							break
						}
					}
					if inAQueue { // only add completed if it's in a queue
						inCompletedQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, "anklet/jobs/github/completed/"+pluginConfig.Owner)
						if err != nil {
							logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
							return
						}
						if inCompletedQueueJobJSON == "" {
							// push it to the queue
							wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
							if err != nil {
								logger.ErrorContext(pluginCtx, "error converting job payload to JSON", "error", err)
								return
							}
							push := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/completed/"+pluginConfig.Owner, wrappedPayloadJSON)
							if push.Err() != nil {
								logger.ErrorContext(pluginCtx, "error pushing job to queue", "error", push.Err())
								return
							}
							logger.InfoContext(pluginCtx, "job pushed to completed queue",
								"workflowJob.ID", *simplifiedWorkflowJobEvent.WorkflowJob.ID,
								"workflowJob.Name", *simplifiedWorkflowJobEvent.WorkflowJob.Name,
								"workflowJob.RunID", *simplifiedWorkflowJobEvent.WorkflowJob.RunID,
								"html_url", *simplifiedWorkflowJobEvent.WorkflowJob.HTMLURL,
								"status", *simplifiedWorkflowJobEvent.WorkflowJob.Status,
								"conclusion", *simplifiedWorkflowJobEvent.WorkflowJob.Conclusion,
								"started_at", *simplifiedWorkflowJobEvent.WorkflowJob.StartedAt,
								"completed_at", *simplifiedWorkflowJobEvent.WorkflowJob.CompletedAt,
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
					// 		wrappedJobPayload := map[string]any{
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

	// if !pluginConfig.SkipRedeliver {
	// 	// Redeliver queued jobs
	// 	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	// 	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	// 	limitForHooks := time.Now().Add(-time.Hour * time.Duration(pluginConfig.RedeliverHours)) // the time we want the stop search for redeliveries
	// 	opts := &github.ListCursorOptions{PerPage: 10}
	// 	logger.InfoContext(pluginCtx, fmt.Sprintf("listing hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", pluginConfig.RedeliverHours))
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
	// 				hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, opts)
	// 				if err != nil {
	// 					return nil, nil, err
	// 				}
	// 				return &hookDeliveries, response, nil
	// 			})
	// 		} else {
	// 			pluginCtx, hookDeliveries, response, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
	// 				hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, opts)
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
	// 						gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
	// 						if err != nil {
	// 							return nil, nil, err
	// 						}
	// 						return gottenHookDelivery, response, nil
	// 					})
	// 				} else {
	// 					pluginCtx, gottenHookDelivery, _, err = ExecuteGitHubClientFunction(pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
	// 						gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
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
	// 				allHooks = append(allHooks, map[string]any{
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

	if !pluginConfig.SkipRedeliver {
		// Redeliver queued jobs
		githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
		pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
		limitForHooks := time.Now().Add(-time.Hour * time.Duration(pluginConfig.RedeliverHours)) // the time we want the stop search for redeliveries
		opts := &github.ListCursorOptions{PerPage: 10}
		logger.InfoContext(pluginCtx, fmt.Sprintf("listing hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", pluginConfig.RedeliverHours))
		// var response *github.Response
		var err error
		if isRepoSet {
			pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
				hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, opts)
				if err != nil {
					return nil, nil, err
				}
				return &hookDeliveries, response, nil
			})
		} else {
			pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
				hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, opts)
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
	// get all keys from database for the main queue and service queues as well as completed
	queuedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"*").Result()
	if queuedKeys == nil && err != nil {
		return pluginCtx, fmt.Errorf("error getting list of queued keys: %s %s", err.Error(), "anklet/jobs/github/queued/"+pluginConfig.Owner+"*")
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
	completedKeys, err := databaseContainer.Client.Keys(pluginCtx, "anklet/jobs/github/completed"+pluginConfig.Owner+"*").Result()
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error getting list of keys", "err", err)
		return pluginCtx, fmt.Errorf("error getting list of completed keys: %s", err.Error())
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
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		} else {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
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
		var workflowJobEventPayload internalGithub.QueueJob
		err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEventPayload)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
			return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
		}
		workflowJob := workflowJobEventPayload.WorkflowJob

		inQueued := false
		// inQueuedListKey := ""
		// inQueuedListIndex := 0
		inCompleted := false
		inCompletedListKey := ""
		inCompletedIndex := 0

		// logger.DebugContext(pluginCtx, "hookDelivery", "hookDelivery", hookDelivery)
		// logger.DebugContext(pluginCtx, "workflowJobEvent", "workflowJobEvent", workflowJobEvent)

		// Check if the job is already in the queue
		if workflowJob.ID == nil {
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
				wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](queuedJob)
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
				if workflowJob.ID == nil {
					continue
				}
				if *wrappedPayload.WorkflowJob.ID == *workflowJob.ID {
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
					wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](completedJob)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
						return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
					}
					if typeErr != nil { // not the type we want
						continue
					}
					if *wrappedPayload.WorkflowJob.ID == *workflowJob.ID {
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
				// logger.ErrorContext(pluginCtx, "error removing completedJob from anklet/jobs/github/completed/"+pluginConfig.Owner, "err", err, "completedJob", allCompletedJobs[inCompletedListKey][inCompletedIndex])
				return pluginCtx, fmt.Errorf("error removing completedJob from anklet/jobs/github/completed/"+pluginConfig.Owner+": %s", err.Error())
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
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return otherGottenHookDelivery, response, nil
						})
					} else {
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
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
					var otherWorkflowJobEventPayload internalGithub.QueueJob
					err = json.Unmarshal(*otherGottenHookDelivery.Request.RawPayload, &otherWorkflowJobEventPayload)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
						return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
					}
					otherWorkflowJob := otherWorkflowJobEventPayload.WorkflowJob
					if *workflowJob.ID == *otherWorkflowJob.ID {
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
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Repositories.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
		} else {
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Organizations.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
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
			"workflowJob.ID", *workflowJob.ID,
			"hookDelivery.Action", *hookDelivery.Action,
			"hookDelivery.StatusCode", *hookDelivery.StatusCode,
			"hookDelivery.Redelivery", *hookDelivery.Redelivery,
			"inCompleted", inCompleted,
		)
	}

	// notify the main thread that the service has started
	select {
	case <-workerGlobals.FirstPluginStarted:
	default:
		close(workerGlobals.FirstPluginStarted)
	}
	logger.InfoContext(pluginCtx, "started plugin")
	metrics.UpdatePlugin(workerCtx, pluginCtx, logger, metrics.PluginBase{
		Status:      "running",
		StatusSince: time.Now(),
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
