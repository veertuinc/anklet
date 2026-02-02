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

	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/anka"
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

// Start runs the HTTP server
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

	if pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in %s:plugins:%s<token/private_key>", configFileName, pluginConfig.Name)
	}
	if strings.HasPrefix(pluginConfig.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: %s", err.Error())
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}
	if pluginConfig.Owner == "" {
		return pluginCtx, fmt.Errorf("owner is not set in %s:plugins:%s<owner>", configFileName, pluginConfig.Name)
	}
	if pluginConfig.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in %s:plugins:%s<secret>", configFileName, pluginConfig.Name)
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: %s", err.Error())
	}

	var githubClient *github.Client
	githubClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		pluginConfig.PrivateKey,
		pluginConfig.AppID,
		pluginConfig.InstallationID,
		pluginConfig.Token,
	)
	if err != nil {
		return pluginCtx, fmt.Errorf("error authenticating github client: %s", err.Error())
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
		payload, err := github.ValidatePayload(r, []byte(pluginConfig.Secret))
		if err != nil {
			logging.Error(pluginCtx, "error validating payload", "error", err)
			return
		}
		event, err := github.ParseWebHook(github.WebHookType(r), payload)
		if err != nil {
			logging.Error(pluginCtx, "error parsing event", "error", err)
			return
		}
		deliveryID := r.Header.Get("X-GitHub-Delivery")
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
					Name:       workflowJob.Repo.Name,
					Owner:      workflowJob.Repo.Owner.Login,
					Visibility: workflowJob.Repo.Visibility,
					Private:    workflowJob.Repo.Private,
				},
				AnkaVM:   anka.VM{},
				Attempts: 0,
			}
			// Create a fresh context for this webhook request to avoid accumulating job contexts
			webhookCtx := logging.AppendCtx(pluginCtx, slog.Group("job",
				slog.Group("workflowJob",
					slog.Any("labels", simplifiedWorkflowJobEvent.WorkflowJob.Labels),
					slog.Any("id", simplifiedWorkflowJobEvent.WorkflowJob.ID),
					slog.Any("name", simplifiedWorkflowJobEvent.WorkflowJob.Name),
					slog.Any("runID", simplifiedWorkflowJobEvent.WorkflowJob.RunID),
					slog.Any("htmlURL", simplifiedWorkflowJobEvent.WorkflowJob.HTMLURL),
					slog.Any("status", simplifiedWorkflowJobEvent.WorkflowJob.Status),
					slog.Any("conclusion", simplifiedWorkflowJobEvent.WorkflowJob.Conclusion),
					slog.Any("startedAt", simplifiedWorkflowJobEvent.WorkflowJob.StartedAt),
					slog.Any("completedAt", simplifiedWorkflowJobEvent.WorkflowJob.CompletedAt),
					slog.Any("workflowName", simplifiedWorkflowJobEvent.WorkflowJob.WorkflowName),
				),
				slog.String("action", simplifiedWorkflowJobEvent.Action),
				slog.Any("repository", simplifiedWorkflowJobEvent.Repository),
				slog.Any("ankaVM", simplifiedWorkflowJobEvent.AnkaVM),
			))
			webhookCtx = logging.AppendCtx(webhookCtx, slog.String("deliveryID", deliveryID))

			logging.Info(webhookCtx, "received workflow job to consider")
			if *workflowJob.Action == "queued" {
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					// make sure it doesn't already exist in the main queued queue
					queuedQueueName := "anklet/jobs/github/queued/" + queueOwner
					inQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, queuedQueueName)
					if err != nil {
						logging.Error(webhookCtx, "error searching in queue", "error", err)
						return
					}

					// Also check if it exists in any handler queues
					inHandlerQueue := false
					if inQueueJobJSON == "" && simplifiedWorkflowJobEvent.WorkflowJob.RunID != nil && simplifiedWorkflowJobEvent.WorkflowJob.ID != nil {
						inHandlerQueue, err = internalGithub.CheckIfJobExistsInHandlerQueues(
							pluginCtx,
							*simplifiedWorkflowJobEvent.WorkflowJob.RunID,
							*simplifiedWorkflowJobEvent.WorkflowJob.ID,
							queueOwner,
						)
						if err != nil {
							logging.Error(webhookCtx, "error checking handler queues", "error", err)
							return
						}
					}

					if inQueueJobJSON == "" && !inHandlerQueue { // if it doesn't exist already
						// push it to the queue
						wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
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
			} else if *workflowJob.Action == "in_progress" {
				if workflowJob.WorkflowJob.Conclusion != nil && *workflowJob.WorkflowJob.Conclusion == "cancelled" {
					return
				}
				// store in_progress so we can know if the registration failed
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					// make sure it doesn't already exist
					inProgressQueueName := "anklet/jobs/github/in_progress/" + queueOwner
					inQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, inProgressQueueName)
					if err != nil {
						logging.Error(webhookCtx, "error searching in queue", "error", err)
						return
					}
					if inQueueJobJSON == "" { // if it doesn't exist already
						// push it to the queue
						wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
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
			} else if *workflowJob.Action == "completed" {
				if exists_in_array_partial(simplifiedWorkflowJobEvent.WorkflowJob.Labels, []string{"anka-template"}) {
					queues := []string{}
					// get all keys from database for the main queue and service queues as well as completed
					queuedKeys, err := databaseContainer.RetryKeys(pluginCtx, "anklet/jobs/github/queued/"+queueOwner+"*")
					if err != nil {
						logging.Error(webhookCtx, "error getting list of queued keys (completed)", "error", err)
						return
					}
					queues = append(queues, queuedKeys...)
					results := make(chan bool, len(queues))
					var wg sync.WaitGroup
					for _, queue := range queues {
						wg.Add(1)
						go func(queue string) {
							defer wg.Done()
							inQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, queue)
							if err != nil {
								logging.Warn(webhookCtx, err.Error(), "queue", queue)
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
						completedQueueName := "anklet/jobs/github/completed/" + queueOwner
						inCompletedQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *simplifiedWorkflowJobEvent.WorkflowJob.ID, completedQueueName)
						if err != nil {
							logging.Error(webhookCtx, "error searching in queue", "error", err)
							return
						}
						if inCompletedQueueJobJSON == "" {
							// push it to the queue
							wrappedPayloadJSON, err := json.Marshal(simplifiedWorkflowJobEvent)
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
							"job_id", simplifiedWorkflowJobEvent.WorkflowJob.ID,
							"queues_checked", queues,
						)
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
	// 			logger.ErrorContext(pluginCtx, "error listing hooks", "error", err)
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
	// 		// 	logger.ErrorContext(pluginCtx, "error marshalling hook deliveries to JSON", "error", err)
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
	// 					logger.ErrorContext(pluginCtx, "error listing hooks", "error", err)
	// 					return
	// 				}
	// 				var workflowJobEvent github.WorkflowJobEvent
	// 				err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEvent)
	// 				if err != nil {
	// 					logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "error", err)
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

	var allHookDeliveries []*github.HookDelivery
	toRedeliver := []*github.HookDelivery{}

	if !pluginConfig.SkipRedeliver {
		// Redeliver queued jobs
		githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
		pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
		limitForHooks := time.Now().Add(-time.Hour * time.Duration(pluginConfig.RedeliverHours)) // the time we want the stop search for redeliveries
		opts := &github.ListCursorOptions{PerPage: 100}                                          // Use max page size to minimize API calls
		logging.Info(pluginCtx, fmt.Sprintf("listing hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", pluginConfig.RedeliverHours))

		reachedLimitTime := false
		apiCallCount := 0
		for !reachedLimitTime {
			apiCallCount++
			var hookDeliveries *[]*github.HookDelivery
			var response *github.Response
			var err error
			if pluginConfig.Repo != "" {
				pluginCtx, hookDeliveries, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*[]*github.HookDelivery, *github.Response, error) {
					hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, opts)
					if err != nil {
						return nil, nil, err
					}
					return &hookDeliveries, response, nil
				})
			} else {
				pluginCtx, hookDeliveries, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*[]*github.HookDelivery, *github.Response, error) {
					hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, opts)
					if err != nil {
						return nil, nil, err
					}
					return &hookDeliveries, response, nil
				})
			}
			if err != nil {
				return pluginCtx, fmt.Errorf("error listing hooks: %s", err.Error())
			}

			for _, hookDelivery := range *hookDeliveries {
				if hookDelivery.Action == nil { // this prevents webhooks like the ping which might be in the list from causing errors since they dont have an action
					continue
				}
				if limitForHooks.After(hookDelivery.DeliveredAt.Time) {
					reachedLimitTime = true
					break
				}
				allHookDeliveries = append(allHookDeliveries, hookDelivery)
			}

			if response.Cursor == "" {
				break
			}
			opts.Cursor = response.Cursor
		}

		logging.Info(pluginCtx, "finished fetching hook deliveries",
			"api_calls_made", apiCallCount,
			"total_deliveries_found", len(allHookDeliveries),
		)

		for _, hookDelivery := range allHookDeliveries {
			if hookDelivery.StatusCode != nil && *hookDelivery.StatusCode != 200 && !*hookDelivery.Redelivery && *hookDelivery.Action != "in_progress" {
				logging.Debug(pluginCtx, "found failed hook delivery, checking if it needs redelivery",
					"hook_id", *hookDelivery.ID,
					"guid", *hookDelivery.GUID,
					"action", *hookDelivery.Action,
					"status_code", *hookDelivery.StatusCode,
					"delivered_at", hookDelivery.DeliveredAt.Time,
				)
				var found *github.HookDelivery
				for _, otherHookDelivery := range allHookDeliveries {
					if hookDelivery.ID != nil && otherHookDelivery.ID != nil && *hookDelivery.ID != *otherHookDelivery.ID &&
						otherHookDelivery.GUID != nil && hookDelivery.GUID != nil && *otherHookDelivery.GUID == *hookDelivery.GUID &&
						otherHookDelivery.Redelivery != nil && *otherHookDelivery.Redelivery &&
						otherHookDelivery.StatusCode != nil && *otherHookDelivery.StatusCode == 200 &&
						otherHookDelivery.DeliveredAt.After(hookDelivery.DeliveredAt.Time) {
						found = otherHookDelivery
						logging.Debug(pluginCtx, "found successful redelivery for failed hook, skipping",
							"original_hook_id", *hookDelivery.ID,
							"redelivery_hook_id", *otherHookDelivery.ID,
							"guid", *hookDelivery.GUID,
						)
						break
					}
				}
				if found != nil {
					continue
				} else {
					// schedule for redelivery
					logging.Debug(pluginCtx, "no successful redelivery found, scheduling for redelivery",
						"hook_id", *hookDelivery.ID,
						"guid", *hookDelivery.GUID,
						"action", *hookDelivery.Action,
					)
					toRedeliver = append(toRedeliver, hookDelivery)
				}
			}
		}
	}

	// if len(allHooks) > 0 {
	// get all keys from database for the main queue and service queues as well as completed
	queuedKeys, err := databaseContainer.RetryKeys(pluginCtx, "anklet/jobs/github/queued/"+queueOwner+"*")
	if queuedKeys == nil && err != nil {
		return pluginCtx, fmt.Errorf("error getting list of queued keys: %s %s", err.Error(), "anklet/jobs/github/queued/"+queueOwner+"*")
	}
	var allQueuedJobs = make(map[string][]string)
	for _, key := range queuedKeys {
		queuedJobs, err := databaseContainer.RetryLRange(pluginCtx, key, 0, -1)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error getting list of queued jobs for key: "+key, "error", err)
			return pluginCtx, fmt.Errorf("error getting list of queued jobs for key: %s", err.Error())
		}
		allQueuedJobs[key] = queuedJobs
	}
	completedKeys, err := databaseContainer.RetryKeys(pluginCtx, "anklet/jobs/github/completed/"+queueOwner+"*")
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error getting list of keys", "error", err)
		return pluginCtx, fmt.Errorf("error getting list of completed keys: %s", err.Error())
	}
	var allCompletedJobs = make(map[string][]string)
	for _, key := range completedKeys {
		completedJobs, err := databaseContainer.RetryLRange(pluginCtx, key, 0, -1)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error getting list of queued jobs for key: "+key, "error", err)
			return pluginCtx, fmt.Errorf("error getting list of queued jobs for key: %s", err.Error())
		}
		allCompletedJobs[key] = completedJobs
	}

	logging.Info(pluginCtx, "processing hooks scheduled for redelivery", "total_to_redeliver", len(toRedeliver))
	redeliveryRequested := false

MainLoop:
	for i := len(toRedeliver) - 1; i >= 0; i-- { // make sure we process/redeliver queued before completed
		hookDelivery := toRedeliver[i]

		logging.Debug(pluginCtx, "processing hook for redelivery",
			"hook_id", *hookDelivery.ID,
			"guid", *hookDelivery.GUID,
			"action", *hookDelivery.Action,
			"status_code", *hookDelivery.StatusCode,
		)

		var gottenHookDelivery *github.HookDelivery
		var err error
		if pluginConfig.Repo != "" {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		} else {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		}
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error listing hooks", "error", err)
			return pluginCtx, fmt.Errorf("error listing hooks: %s", err.Error())
		}
		var workflowJobEventPayload internalGithub.QueueJob
		err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEventPayload)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "error", err)
			return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
		}
		workflowJob := workflowJobEventPayload.WorkflowJob

		logging.Debug(pluginCtx, "fetched hook delivery details",
			"hook_id", *hookDelivery.ID,
			"workflow_job_id", *workflowJob.ID,
			"workflow_job_name", *workflowJob.Name,
			"labels", workflowJob.Labels,
		)

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
			logging.Warn(pluginCtx, "WorkflowJob or WorkflowJob.ID is nil")
			continue
		}

		// Queued deliveries
		// // always get queued jobs so that completed cleanup (when there is no queued but there is a completed) works
		logging.Debug(pluginCtx, "checking if job is in queued database", "workflow_job_id", *workflowJob.ID)
		for _, queuedJobs := range allQueuedJobs {
			for _, queuedJob := range queuedJobs {
				if queuedJob == "" {
					continue
				}
				wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](queuedJob)
				if err != nil {
					// logger.ErrorContext(pluginCtx, "error unmarshalling job", "error", err)
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
					logging.Debug(pluginCtx, "job found in queued database", "workflow_job_id", *workflowJob.ID)
					// inQueuedListKey = key
					// inQueuedListIndex = index
					break
				}
			}
		}
		if !inQueued {
			logging.Debug(pluginCtx, "job not found in queued database", "workflow_job_id", *workflowJob.ID)
		}

		// Completed deliveries
		if *hookDelivery.Action == "completed" {
			logging.Debug(pluginCtx, "checking if completed job is in completed database", "workflow_job_id", *workflowJob.ID)
			for key, completedJobs := range allCompletedJobs {
				for index, completedJob := range completedJobs {
					wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](completedJob)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling job", "error", err)
						return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
					}
					if typeErr != nil { // not the type we want
						continue
					}
					if *wrappedPayload.WorkflowJob.ID == *workflowJob.ID {
						inCompleted = true
						inCompletedListKey = key
						inCompletedIndex = index
						logging.Debug(pluginCtx, "job found in completed database", "workflow_job_id", *workflowJob.ID)
						break
					}
				}
			}
			if !inCompleted {
				logging.Debug(pluginCtx, "completed job not found in completed database", "workflow_job_id", *workflowJob.ID)
			}
		}

		// if in queued, but also has completed; continue and do nothing
		if inQueued && inCompleted {
			logging.Debug(pluginCtx, "job is in both queued and completed, skipping redelivery",
				"workflow_job_id", *workflowJob.ID,
				"hook_id", *hookDelivery.ID,
			)
			continue
		}

		// if in completed, but has no queued; remove from completed db
		if inCompleted && !inQueued {
			logging.Debug(pluginCtx, "job is in completed but not queued, removing from completed database",
				"workflow_job_id", *workflowJob.ID,
				"hook_id", *hookDelivery.ID,
			)
			_, err = databaseContainer.RetryLRem(pluginCtx, inCompletedListKey, 1, allCompletedJobs[inCompletedListKey][inCompletedIndex])
			if err != nil {
				// logger.ErrorContext(pluginCtx, "error removing completedJob from anklet/jobs/github/completed/"+queueOwner, "error", err, "completedJob", allCompletedJobs[inCompletedListKey][inCompletedIndex])
				return pluginCtx, fmt.Errorf("error removing completedJob from anklet/jobs/github/completed/"+queueOwner+": %s", err.Error())
			}
			continue
		}

		// handle queued that have already been successfully delivered before.
		if *hookDelivery.Action == "queued" {
			logging.Debug(pluginCtx, "checking if queued hook already has a completed delivery",
				"hook_id", *hookDelivery.ID,
				"workflow_job_id", *workflowJob.ID,
			)
			// check if a completed hook exists, so we don't re-queue something already finished
			for _, otherHookDelivery := range allHookDeliveries {
				if *otherHookDelivery.Action == "completed" &&
					otherHookDelivery.DeliveredAt != nil && otherHookDelivery.DeliveredAt.After(hookDelivery.DeliveredAt.Time) &&
					otherHookDelivery.RepositoryID != nil && *otherHookDelivery.RepositoryID == *hookDelivery.RepositoryID {
					logging.Debug(pluginCtx, "found completed hook after queued hook in same repo, checking if same workflow job",
						"queued_hook_id", *hookDelivery.ID,
						"completed_hook_id", *otherHookDelivery.ID,
						"queued_delivered_at", hookDelivery.DeliveredAt.Time,
						"completed_delivered_at", otherHookDelivery.DeliveredAt.Time,
					)
					var otherGottenHookDelivery *github.HookDelivery
					var err error
					if pluginConfig.Repo != "" {
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *otherHookDelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return otherGottenHookDelivery, response, nil
						})
					} else {
						pluginCtx, otherGottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
							otherGottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *otherHookDelivery.ID)
							if err != nil {
								return nil, nil, err
							}
							return otherGottenHookDelivery, response, nil
						})
					}
					if err != nil {
						logging.Warn(pluginCtx, "error getting hook delivery, skipping check for completed delivery",
							"error", err,
							"hook_id", *otherHookDelivery.ID,
							"workflow_job_id", *workflowJob.ID,
						)
						continue
					}
					var otherWorkflowJobEventPayload internalGithub.QueueJob
					err = json.Unmarshal(*otherGottenHookDelivery.Request.RawPayload, &otherWorkflowJobEventPayload)
					if err != nil {
						// logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "error", err)
						return pluginCtx, fmt.Errorf("error unmarshalling hook request raw payload to HookResponse: %s", err.Error())
					}
					otherWorkflowJob := otherWorkflowJobEventPayload.WorkflowJob
					if *workflowJob.ID == *otherWorkflowJob.ID {
						logging.Debug(pluginCtx, "found completed delivery for queued hook, skipping redelivery",
							"hook_id", *hookDelivery.ID,
							"workflow_job_id", *workflowJob.ID,
							"completed_hook_id", *otherHookDelivery.ID,
							"other_workflow_job_id", *otherWorkflowJob.ID,
						)
						continue MainLoop
					} else {
						logging.Debug(pluginCtx, "completed hook is for different workflow job, continuing",
							"queued_workflow_job_id", *workflowJob.ID,
							"completed_workflow_job_id", *otherWorkflowJob.ID,
						)
					}
				}
			}
		}

		// if in queued, and also has a successful completed, something is wrong and we need to re-deliver it.
		if *hookDelivery.Action == "completed" && inQueued && *hookDelivery.StatusCode == 200 && !inCompleted {
			logging.Info(pluginCtx, "hook delivery has completed but is still in queued; redelivering",
				"hook_id", *hookDelivery.ID,
				"workflow_job_id", *workflowJob.ID,
			)
		} else if *hookDelivery.StatusCode == 200 || inCompleted { // all other cases (like when it's queued); continue
			logging.Debug(pluginCtx, "skipping redelivery, hook already succeeded or completed",
				"hook_id", *hookDelivery.ID,
				"workflow_job_id", *workflowJob.ID,
				"status_code", *hookDelivery.StatusCode,
				"in_completed", inCompleted,
			)
			continue
		}

		// Note; We cannot (and probably should not) stop completed from being redelivered.

		logging.Info(pluginCtx, "redelivering hook",
			"hook_id", *hookDelivery.ID,
			"workflow_job_id", *workflowJob.ID,
			"action", *hookDelivery.Action,
			"original_status_code", *hookDelivery.StatusCode,
			"in_queued", inQueued,
			"in_completed", inCompleted,
		)

		// Redeliver the hook
		var redelivery *github.HookDelivery
		if pluginConfig.Repo != "" {
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Repositories.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
		} else {
			pluginCtx, redelivery, _, _ = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.HookDelivery, *github.Response, error) {
				redelivery, response, err := githubClient.Organizations.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return redelivery, response, nil
			})
		}
		// err doesn't matter here and it will always throw "job scheduled on GitHub side; try again later"
		redeliveryRequested = true
		logging.Info(pluginCtx, "hook redelivery requested successfully",
			"redelivery", redelivery,
			"hookDelivery", map[string]any{
				"guid":       *hookDelivery.GUID,
				"action":     *hookDelivery.Action,
				"statusCode": *hookDelivery.StatusCode,
				"redelivery": *hookDelivery.Redelivery,
			},
			"job", map[string]any{
				"workflowJob": map[string]any{
					"id": *workflowJob.ID,
				},
			},
			"inCompleted", inCompleted,
		)
	}

	logging.Info(pluginCtx, "finished processing hooks for redelivery",
		"total_hooks_checked", len(toRedeliver),
	)

	if redeliveryRequested {
		logging.Info(pluginCtx, "sleeping for 1 minute to allow handlers to process jobs")
		time.Sleep(1 * time.Minute)
	}

	// Clean up in_progress queue AFTER redelivery processing completes
	// This ensures any in_progress webhooks have a chance to be redelivered first
	_, err = databaseContainer.RetryDel(pluginCtx, "anklet/jobs/github/in_progress/"+queueOwner)
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
