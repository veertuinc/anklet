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

	if err := pluginConfig.ValidateGitHubScope(); err != nil {
		return pluginCtx, fmt.Errorf("invalid github scope in %s:plugins:%s: %w", configFileName, pluginConfig.Name, err)
	}
	if pluginConfig.Secret == "" {
		return pluginCtx, fmt.Errorf("secret is not set in %s:plugins:%s<secret>", configFileName, pluginConfig.Name)
	}
	// Enterprise Cloud has no public hook-delivery REST API; redelivery is UI-only.
	// Webhook ingest only needs the shared secret — no GitHub App/PAT auth required.
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
			return pluginCtx, fmt.Errorf("unable to get user home directory: %s", err.Error())
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database client from context: %s", err.Error())
	}

	var githubClient *github.Client
	var githubWrapperClient *internalGithub.GitHubClientWrapper
	if !pluginConfig.IsEnterpriseScope() {
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
			return pluginCtx, fmt.Errorf("error authenticating github client: %s", err.Error())
		}
		pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	} else {
		logging.Debug(pluginCtx, "enterprise receiver skipping GitHub API authentication (webhook secret only)")
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
			if simplifiedWorkflowJobEvent.WorkflowJob.ID != nil {
				webhookCtx = logging.AppendCtx(webhookCtx, slog.Int64("workflowJobID", *simplifiedWorkflowJobEvent.WorkflowJob.ID))
			}
			if simplifiedWorkflowJobEvent.WorkflowJob.RunID != nil {
				webhookCtx = logging.AppendCtx(webhookCtx, slog.Int64("workflowJobRunID", *simplifiedWorkflowJobEvent.WorkflowJob.RunID))
			}
			if simplifiedWorkflowJobEvent.WorkflowJob.WorkflowName != nil {
				webhookCtx = logging.AppendCtx(webhookCtx, slog.String("workflowName", *simplifiedWorkflowJobEvent.WorkflowJob.WorkflowName))
			}
			if simplifiedWorkflowJobEvent.Repository.Name != nil && simplifiedWorkflowJobEvent.Repository.Owner != nil {
				webhookCtx = logging.AppendCtx(webhookCtx, slog.String("repository", *simplifiedWorkflowJobEvent.Repository.Owner+"/"+*simplifiedWorkflowJobEvent.Repository.Name))
			}

			logging.Info(webhookCtx, "received workflow job to consider")
			if workflowJob.WorkflowJob.HTMLURL != nil {
				logging.Info(webhookCtx, "workflow job HTML URL", "workflowJobHTMLURL", *workflowJob.WorkflowJob.HTMLURL)
			}
			if workflowJob.WorkflowJob.Status != nil {
				logging.Info(webhookCtx, "workflow job status", "workflowJobStatus", *workflowJob.WorkflowJob.Status)
			}
			if workflowJob.WorkflowJob.Conclusion != nil {
				logging.Info(webhookCtx, "workflow job conclusion", "workflowJobConclusion", *workflowJob.WorkflowJob.Conclusion)
			}
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
						logging.Info(webhookCtx, "job pushed to queued queue",
							"queue", queuedQueueName,
							"queue_length", queueLength,
						)
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

	for {
		select {
		case <-workerCtx.Done():
			logging.Warn(pluginCtx, "shutting down receiver")
			redeliveryWG.Wait()
			if err := server.Shutdown(pluginCtx); err != nil {
				return pluginCtx, fmt.Errorf("receiver shutdown error: %s", err.Error())
			}
			return pluginCtx, nil
		case <-time.After(time.Second * 1):
			continue
		}
	}
}
