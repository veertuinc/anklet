package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v63/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

type WorkflowRunJobDetail struct {
	JobID           int64
	JobName         string
	JobURL          string
	WorkflowName    string
	AnkaTemplate    string
	AnkaTemplateTag string
	RunID           int64
	UniqueID        string
	Labels          []string
	Repo            string
}

// func exists_in_array_exact(array_to_search_in []string, desired []string) bool {
// 	for _, desired_string := range desired {
// 		found := false
// 		for _, item := range array_to_search_in {
// 			if item == desired_string {
// 				found = true
// 				break
// 			}
// 		}
// 		if !found {
// 			return false
// 		}
// 	}
// 	return true
// }

// func exists_in_array_regex(array_to_search_in []string, desired []string) bool {
// 	if len(desired) == 0 || desired[0] == "" {
// 		return false
// 	}
// 	for _, desired_string := range desired {
// 		// fmt.Printf("  desired_string: %s\n", desired_string)
// 		found := false
// 		for _, item := range array_to_search_in {
// 			// fmt.Printf("    item: %s\n", item)
// 			// Check if the desired_string is a valid regex pattern
// 			if rege, err := regexp.Compile(desired_string); err == nil {
// 				// If it's a valid regex, check for a regex match
// 				sanitizedSplit := slices.DeleteFunc(rege.Split(item, -1), func(e string) bool {
// 					return e == ""
// 				})
// 				// fmt.Printf("    sanitizedSplit: %+v\n", sanitizedSplit)
// 				if len(sanitizedSplit) == 0 {
// 					// fmt.Println("      regex match")
// 					found = true
// 					break
// 				}
// 			}
// 		}
// 		if !found {
// 			return false
// 		}
// 	}
// 	return true
// }

// func does_not_exist_in_array_regex(array_to_search_in []string, excluded []string) bool {
// 	if len(excluded) == 0 || excluded[0] == "" {
// 		return true
// 	}
// 	for _, excluded_string := range excluded {
// 		// fmt.Printf("  excluded_string: %s\n", excluded_string)
// 		found := false
// 		for _, item := range array_to_search_in {
// 			// fmt.Printf("    item: %s\n", item)
// 			// Check if the desired_string is a valid regex pattern
// 			if rege, err := regexp.Compile(excluded_string); err == nil {
// 				// If it's a valid regex, check for a regex match
// 				sanitizedSplit := slices.DeleteFunc(rege.Split(item, -1), func(e string) bool {
// 					return e == ""
// 				})
// 				// fmt.Printf("    sanitizedSplit: %+v\n", sanitizedSplit)
// 				if len(sanitizedSplit) > 0 {
// 					// fmt.Println("      regex match")
// 					found = true
// 					break
// 				}
// 			}
// 		}
// 		if !found {
// 			return false
// 		}
// 	}
// 	return true
// }

func extractLabelValue(labels []string, prefix string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix)
		}
	}
	return ""
}

func sendCancelWorkflowRun(pluginCtx context.Context, logger *slog.Logger, workflow WorkflowRunJobDetail) error {
	githubClient := internalGithub.GetGitHubClientFromContext(pluginCtx)
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	cancelSent := false
	for {
		pluginCtx, workflowRun, _, err := executeGitHubClientFunction[github.WorkflowRun](pluginCtx, logger, func() (*github.WorkflowRun, *github.Response, error) {
			workflowRun, resp, err := githubClient.Actions.GetWorkflowRunByID(context.Background(), ctxPlugin.Owner, workflow.Repo, workflow.RunID)
			return workflowRun, resp, err
		})
		if err != nil {
			logger.ErrorContext(pluginCtx, "error getting workflow run by ID", "err", err)
			return err
		}
		if *workflowRun.Status == "completed" ||
			(workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") ||
			cancelSent {
			break
		} else {
			logger.WarnContext(pluginCtx, "workflow run is still active... waiting for cancellation so we can clean up...", "workflow_run_id", workflow.RunID)
			if !cancelSent { // this has to happen here so that it doesn't error with "409 Cannot cancel a workflow run that is completed. " if the job is already cancelled
				pluginCtx, cancelResponse, _, cancelErr := executeGitHubClientFunction[github.Response](pluginCtx, logger, func() (*github.Response, *github.Response, error) {
					resp, err := githubClient.Actions.CancelWorkflowRunByID(context.Background(), ctxPlugin.Owner, workflow.Repo, workflow.RunID)
					return resp, nil, err
				})
				// don't use cancelResponse.Response.StatusCode or else it'll error with SIGSEV
				if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
					logger.ErrorContext(pluginCtx, "error executing githubClient.Actions.CancelWorkflowRunByID", "err", cancelErr, "response", cancelResponse)
					return cancelErr
				}
				cancelSent = true
				logger.WarnContext(pluginCtx, "sent cancel workflow run", "workflow_run_id", workflow.RunID)
			}
			time.Sleep(10 * time.Second)
		}
	}
	return nil
}

// https://github.com/gofri/go-github-ratelimit has yet to support primary rate limits, so we have to do it ourselves.
func executeGitHubClientFunction[T any](pluginCtx context.Context, logger *slog.Logger, executeFunc func() (*T, *github.Response, error)) (context.Context, *T, *github.Response, error) {
	result, response, err := executeFunc()
	if response != nil {
		pluginCtx = logging.AppendCtx(pluginCtx, slog.Int("api_limit_remaining", response.Rate.Remaining))
		pluginCtx = logging.AppendCtx(pluginCtx, slog.String("api_limit_reset_time", response.Rate.Reset.Time.Format(time.RFC3339)))
		pluginCtx = logging.AppendCtx(pluginCtx, slog.Int("api_limit", response.Rate.Limit))
		if response.Rate.Remaining <= 10 { // handle primary rate limiting
			sleepDuration := time.Until(response.Rate.Reset.Time) + time.Second // Adding a second to ensure we're past the reset time
			logger.WarnContext(pluginCtx, "GitHub API rate limit exceeded, sleeping until reset")
			metricsData := metrics.GetMetricsDataFromContext(pluginCtx)
			metricsData.SetStatus(pluginCtx, logger, "limit_paused")
			select {
			case <-time.After(sleepDuration):
				metricsData.SetStatus(pluginCtx, logger, "running")
				return executeGitHubClientFunction(pluginCtx, logger, executeFunc) // Retry the function after waiting
			case <-pluginCtx.Done():
				return pluginCtx, nil, nil, pluginCtx.Err()
			}
		}
	}
	if err != nil {
		if err.Error() != "context canceled" {
			if !strings.Contains(err.Error(), "try again later") {
				logger.Error("error executing GitHub client function: " + err.Error())
			}
		}
		return pluginCtx, nil, nil, err
	}
	return pluginCtx, result, response, nil
}

func CheckForCompletedJobs(
	workerCtx context.Context,
	pluginCtx context.Context,
	logger *slog.Logger,
	checkForCompletedJobsMu *sync.Mutex,
	completedJobChannel chan github.WorkflowJobEvent,
	ranOnce chan struct{},
	runOnce bool,
	failureChannel chan bool,
) {
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		logging.Panic(workerCtx, pluginCtx, "error getting database from context")
	}
	defer func() {
		if checkForCompletedJobsMu != nil {
			checkForCompletedJobsMu.Unlock()
		}
		// ensure, outside of needing to return on error, that the following always runs
		select {
		case <-ranOnce:
			// already closed, do nothing
		default:
			close(ranOnce)
		}
	}()
	for {
		// BE VERY CAREFUL when you use return here. You could orphan the job if you're not careful.
		checkForCompletedJobsMu.Lock()
		// do not use 'continue' in the loop or else the ranOnce won't happen
		logger.DebugContext(pluginCtx, "CheckForCompletedJobs "+ctxPlugin.Name+" | runOnce "+fmt.Sprint(runOnce))
		select {
		case <-failureChannel:
			// logger.ErrorContext(pluginCtx, "CheckForCompletedJobs"+ctxPlugin.Name+" failureChannel")
			returnToMainQueue, ok := workerCtx.Value(config.ContextKey("returnToMainQueue")).(chan bool)
			if !ok {
				logger.ErrorContext(pluginCtx, "error getting returnToMainQueue from context")
				return
			}
			returnToMainQueue <- true
			return
		case <-completedJobChannel:
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "CheckForCompletedJobs"+ctxPlugin.Name+" pluginCtx.Done()")
			return
		default:
		}
		// get the job ID
		existingJobString, err := databaseContainer.Client.LIndex(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Name, 0).Result()
		if runOnce && err == redis.Nil { // handle no job for service; needed so the github plugin resets and looks for new jobs again
			logger.ErrorContext(pluginCtx, "CheckForCompletedJobs"+ctxPlugin.Name+" err == redis.Nil")
			return
		} else {
			if err == nil {
				// check if there is already a completed job queued for the service
				// // this can happen if the service crashes or is stopped before it finalizes cleanup
				count, err := databaseContainer.Client.LLen(pluginCtx, "anklet/jobs/github/completed/"+ctxPlugin.Name).Result()
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting count of objects in anklet/jobs/github/completed/"+ctxPlugin.Name, "err", err)
					return
				}
				existingJobEvent, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](existingJobString)
				if err != nil || typeErr != nil {
					logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
					return
				}
				if count > 0 {
					select {
					case completedJobChannel <- existingJobEvent:
					default:
						// remove the completed job we found
						_, err = databaseContainer.Client.Del(pluginCtx, "anklet/jobs/github/completed/"+ctxPlugin.Name).Result()
						if err != nil {
							logger.ErrorContext(pluginCtx, "error removing completedJob from anklet/jobs/github/completed/"+ctxPlugin.Name, "err", err)
							return
						}
					}
				} else {
					completedJobs, err := databaseContainer.Client.LRange(pluginCtx, "anklet/jobs/github/completed", 0, -1).Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error getting list of completed jobs", "err", err)
						return
					}
					if existingJobEvent.WorkflowJob == nil {
						logger.ErrorContext(pluginCtx, "existingJobEvent.WorkflowJob is nil")
						return
					}
					for _, completedJob := range completedJobs {
						completedJobWebhookEvent, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](completedJob)
						if err != nil || typeErr != nil {
							logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
							return
						}
						if *completedJobWebhookEvent.WorkflowJob.ID == *existingJobEvent.WorkflowJob.ID {
							// remove the completed job we found
							_, err = databaseContainer.Client.LRem(pluginCtx, "anklet/jobs/github/completed", 1, completedJob).Result()
							if err != nil {
								logger.ErrorContext(pluginCtx, "error removing completedJob from anklet/jobs/github/completed", "err", err, "completedJob", completedJobWebhookEvent)
								return
							}
							// delete the existing service task
							// _, err = databaseContainer.Client.Del(pluginCtx, serviceQueueDatabaseKeyName).Result()
							// if err != nil {
							// 	logger.ErrorContext(pluginCtx, "error deleting all objects from "+serviceQueueDatabaseKeyName, "err", err)
							// 	return
							// }
							// add a task for the completed job so we know the clean up
							_, err = databaseContainer.Client.LPush(pluginCtx, "anklet/jobs/github/completed/"+ctxPlugin.Name, completedJob).Result()
							if err != nil {
								logger.ErrorContext(pluginCtx, "error inserting completed job into list", "err", err)
								return
							}
							completedJobChannel <- completedJobWebhookEvent
							return
						}
					}
				}
			}
		}
		// ensure, outside of needing to return on error, that the following always runs
		select {
		case <-ranOnce:
			// already closed, do nothing
		default:
			close(ranOnce)
		}
		if runOnce {
			return
		}
		if checkForCompletedJobsMu != nil {
			checkForCompletedJobsMu.Unlock()
		}
		time.Sleep(3 * time.Second)
	}
}

// cleanup will pop off the last item from the list and, based on its type, perform the appropriate cleanup action
// this assumes the plugin code created a list item to represent the thing to clean up
func cleanup(
	workerCtx context.Context,
	pluginCtx context.Context,
	logger *slog.Logger,
	completedJobChannel chan github.WorkflowJobEvent,
	cleanupMu *sync.Mutex,
) {
	cleanupMu.Lock()
	// create an idependent copy of the pluginCtx so we can do cleanup even if pluginCtx got "context canceled"
	cleanupContext := context.Background()
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	returnToMainQueue, ok := workerCtx.Value(config.ContextKey("returnToMainQueue")).(chan bool)
	if !ok {
		logger.ErrorContext(pluginCtx, "error getting returnToMainQueue from context")
		return
	}
	serviceDatabase, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("database"), serviceDatabase)
	cleanupContext, cancel := context.WithCancel(cleanupContext)
	defer func() {
		if cleanupMu != nil {
			cleanupMu.Unlock()
		}
		cancel()
	}()
	databaseContainer, err := database.GetDatabaseFromContext(cleanupContext)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return
	}
	for {
		var jobJSON string
		exists, err := databaseContainer.Client.Exists(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning").Result()
		if err != nil {
			logger.ErrorContext(cleanupContext, "error checking if cleaning up already in progress", "err", err)
		}
		if exists == 1 {
			logger.InfoContext(pluginCtx, "cleaning up already in progress; getting job")
			jobJSON, err = databaseContainer.Client.LIndex(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning", 0).Result()
			if err != nil {
				logger.ErrorContext(pluginCtx, "error getting job from the list", "err", err)
				return
			}
		} else {
			// pop the job from the list and push it to the cleaning list
			jobJSON, err = databaseContainer.Client.RPopLPush(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning").Result()
			if err == redis.Nil {
				return // nothing to clean up
			} else if err != nil {
				logger.ErrorContext(pluginCtx, "error popping job from the list", "err", err)
				return
			}
		}
		var typedJob map[string]interface{}
		if err := json.Unmarshal([]byte(jobJSON), &typedJob); err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return
		}

		var payload map[string]interface{}
		payloadJSON, err := json.Marshal(typedJob)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error marshalling payload", "err", err)
			return
		}
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return
		}
		payloadBytes, err := json.Marshal(payload["payload"])
		if err != nil {
			logger.ErrorContext(pluginCtx, "error marshalling payload", "err", err)
			return
		}
		switch typedJob["type"] {
		case "anka.VM":
			var vm anka.VM
			err = json.Unmarshal(payloadBytes, &vm)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error unmarshalling payload to webhook.WorkflowJobPayload", "err", err)
				return
			}
			ankaCLI := anka.GetAnkaCLIFromContext(pluginCtx)
			ankaCLI.AnkaDelete(workerCtx, pluginCtx, &vm)
			databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning")
			continue // required to keep processing tasks in the db list
		case "WorkflowJobPayload": // MUST COME LAST
			var workflowJobEvent github.WorkflowJobEvent
			err = json.Unmarshal(payloadBytes, &workflowJobEvent)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error unmarshalling payload to webhook.WorkflowJobPayload", "err", err)
				return
			}
			// return it to the queue if the job isn't completed yet
			// if we don't, we could suffer from a situation where a completed job comes in and is orphaned
			select {
			case <-completedJobChannel:
				databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/completed/"+ctxPlugin.Name)
				databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning")
				break // break loop and delete /queued/servicename
			default:
				select {
				case <-returnToMainQueue:
					logger.WarnContext(pluginCtx, "pushing job back to anklet/jobs/github/queued")
					_, err := databaseContainer.Client.RPopLPush(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning", "anklet/jobs/github/queued").Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error pushing job back to queued", "err", err)
						return
					}
					databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning")
				default:
					logger.WarnContext(pluginCtx, "pushing job back to anklet/jobs/github/queued/"+ctxPlugin.Name)
					_, err := databaseContainer.Client.RPopLPush(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning", "anklet/jobs/github/queued/"+ctxPlugin.Name).Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error pushing job back to queued", "err", err)
						return
					}
					databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/queued/"+ctxPlugin.Name+"/cleaning")
				}
			}
		default:
			logger.ErrorContext(pluginCtx, "unknown job type", "job", typedJob)
			return
		}
		return // don't delete the queued/servicename
	}
	// databaseContainer.Client.Del(cleanupContext, "anklet/jobs/github/queued/"+service.Name)
}

func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	logger *slog.Logger,
	metricsData *metrics.MetricsDataLock,
) {
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	isRepoSet := config.GetIsRepoSetFromContext(pluginCtx)

	metricsData.AddPlugin(metrics.Plugin{
		PluginBase: &metrics.PluginBase{
			Name:        ctxPlugin.Name,
			PluginName:  ctxPlugin.Plugin,
			RepoName:    ctxPlugin.Repo,
			OwnerName:   ctxPlugin.Owner,
			Status:      "idle",
			StatusSince: time.Now(),
		},
	})

	logger.InfoContext(pluginCtx, "checking for jobs....")

	if ctxPlugin.Token == "" && ctxPlugin.PrivateKey == "" {
		logging.Panic(workerCtx, pluginCtx, "token and private_key are not set in anklet.yaml:plugins:"+ctxPlugin.Name+":token/private_key")
	}
	if ctxPlugin.PrivateKey != "" && (ctxPlugin.AppID == 0 || ctxPlugin.InstallationID == 0) {
		logging.Panic(workerCtx, pluginCtx, "private_key, app_id, and installation_id must all be set in anklet.yaml:plugins:"+ctxPlugin.Name+"")
	}
	if ctxPlugin.Owner == "" {
		logging.Panic(workerCtx, pluginCtx, "owner is not set in anklet.yaml:plugins:"+ctxPlugin.Name+":owner")
	}
	// if ctxPlugin.Repo == "" {
	// 	logging.Panic(workerCtx, pluginCtx, "repo is not set in anklet.yaml:plugins:"+ctxPlugin.Name+":repo")
	// }

	hostHasVmCapacity := anka.HostHasVmCapacity(pluginCtx)
	if !hostHasVmCapacity {
		logger.DebugContext(pluginCtx, "host does not have vm capacity")
		return
	}

	rateLimiter := internalGithub.GetRateLimitWaiterClientFromContext(pluginCtx)
	httpTransport := config.GetHttpTransportFromContext(pluginCtx)
	var githubClient *github.Client
	if ctxPlugin.PrivateKey != "" {
		itr, err := ghinstallation.NewKeyFromFile(httpTransport, int64(ctxPlugin.AppID), int64(ctxPlugin.InstallationID), ctxPlugin.PrivateKey)
		if err != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "error creating github app installation token", "err", err)
			return
		}
		rateLimiter.Transport = itr
		githubClient = github.NewClient(rateLimiter)
	} else {
		githubClient = github.NewClient(rateLimiter).WithAuthToken(ctxPlugin.Token)
	}

	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	var repositoryURL string
	if isRepoSet {
		pluginCtx = logging.AppendCtx(pluginCtx, slog.String("repo", ctxPlugin.Repo))
		repositoryURL = fmt.Sprintf("https://github.com/%s/%s", ctxPlugin.Owner, ctxPlugin.Repo)
	} else {
		repositoryURL = fmt.Sprintf("https://github.com/%s", ctxPlugin.Owner)
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("owner", ctxPlugin.Owner))

	checkForCompletedJobsMu := &sync.Mutex{}
	cleanupMu := &sync.Mutex{}

	failureChannel := make(chan bool, 1)

	completedJobChannel := make(chan github.WorkflowJobEvent, 1)
	// wait group so we can wait for the goroutine to finish before exiting the service
	var wg sync.WaitGroup
	wg.Add(1)

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return
	}

	defer func() {
		wg.Wait()
		cleanup(workerCtx, pluginCtx, logger, completedJobChannel, cleanupMu)
		close(completedJobChannel)
	}()

	// check constantly for a cancelled webhook to be received for our job
	ranOnce := make(chan struct{})
	go func() {
		CheckForCompletedJobs(workerCtx, pluginCtx, logger, checkForCompletedJobsMu, completedJobChannel, ranOnce, false, failureChannel)
		wg.Done()
	}()
	<-ranOnce // wait for the goroutine to run at least once
	// finalize cleanup if the service crashed mid-cleanup
	cleanup(workerCtx, pluginCtx, logger, completedJobChannel, cleanupMu)
	select {
	case <-completedJobChannel:
		logger.InfoContext(pluginCtx, "completed job found at start")
		completedJobChannel <- github.WorkflowJobEvent{}
		return
	case <-pluginCtx.Done():
		logger.WarnContext(pluginCtx, "context canceled before completed job found")
		return
	default:
	}

	var wrappedPayloadJSON string
	// allow picking up where we left off
	wrappedPayloadJSON, err = databaseContainer.Client.LIndex(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Name, -1).Result()
	if err != nil && err != redis.Nil {
		logger.ErrorContext(pluginCtx, "error getting last object from anklet/jobs/github/queued/"+ctxPlugin.Name, "err", err)
		return
	}
	if wrappedPayloadJSON == "" { // if we haven't done anything before, get something from the main queue
		eldestQueuedJob, err := databaseContainer.Client.LPop(pluginCtx, "anklet/jobs/github/queued").Result()
		if err == redis.Nil {
			logger.DebugContext(pluginCtx, "no queued jobs found")
			completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
			return
		}
		if err != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "error getting queued jobs", "err", err)
			return
		}
		databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Name, eldestQueuedJob)
		wrappedPayloadJSON = eldestQueuedJob
	}

	queuedJob, err, typeErr := database.UnwrapPayload[github.WorkflowJobEvent](wrappedPayloadJSON)
	if err != nil || typeErr != nil {
		logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
		return
	}
	if !isRepoSet {
		pluginCtx = logging.AppendCtx(pluginCtx, slog.String("repo", *queuedJob.Repo.Name))
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobID", *queuedJob.WorkflowJob.ID))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowJobName", *queuedJob.WorkflowJob.Name))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobRunID", *queuedJob.WorkflowJob.RunID))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowName", *queuedJob.WorkflowJob.WorkflowName))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("jobURL", *queuedJob.WorkflowJob.HTMLURL))

	logger.DebugContext(pluginCtx, "queued job found", "queuedJob", queuedJob.Action)

	// check if the job is already completed, so we don't orphan if there is
	// a job in anklet/jobs/github/queued and also a anklet/jobs/github/completed
	CheckForCompletedJobs(workerCtx, pluginCtx, logger, checkForCompletedJobsMu, completedJobChannel, ranOnce, true, failureChannel)
	select {
	case <-completedJobChannel:
		logger.InfoContext(pluginCtx, "completed job found by CheckForCompletedJobs")
		completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
		return
	case <-pluginCtx.Done():
		logger.WarnContext(pluginCtx, "context canceled before completed job found")
		return
	default:
	}

	// get the unique unique-id for this job
	// this ensures that multiple jobs in the same workflow run don't compete for the same runner
	uniqueID := extractLabelValue(queuedJob.WorkflowJob.Labels, "unique-id:")
	if uniqueID == "" {
		logger.WarnContext(pluginCtx, "unique-id label not found or empty; something wrong with your workflow yaml")
		return
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("uniqueID", uniqueID))
	ankaTemplate := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template:")
	if ankaTemplate == "" {
		logger.WarnContext(pluginCtx, "warning: unable to find Anka Template specified in labels - skipping")
		return
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("ankaTemplate", ankaTemplate))
	ankaTemplateTag := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template-tag:")
	if ankaTemplateTag == "" {
		ankaTemplateTag = "(using latest)"
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("ankaTemplateTag", ankaTemplateTag))

	workflowJob := WorkflowRunJobDetail{
		JobID:           *queuedJob.WorkflowJob.ID,
		JobName:         *queuedJob.WorkflowJob.Name,
		JobURL:          *queuedJob.WorkflowJob.HTMLURL,
		WorkflowName:    *queuedJob.WorkflowJob.WorkflowName,
		AnkaTemplate:    ankaTemplate,
		AnkaTemplateTag: ankaTemplateTag,
		RunID:           *queuedJob.WorkflowJob.RunID,
		UniqueID:        uniqueID,
		Labels:          queuedJob.WorkflowJob.Labels,
		Repo:            *queuedJob.Repo.Name,
	}

	// get anka CLI
	ankaCLI := anka.GetAnkaCLIFromContext(pluginCtx)

	logger.InfoContext(pluginCtx, "handling anka workflow run job")
	metricsData.SetStatus(pluginCtx, logger, "running")

	skipPrep := false // allows us to wait for the cancellation we sent to be received so we can clean up properly

	// See if VM Template existing already
	//TODO: be able to interrupt this
	noTemplateTagExistsError, returnToQueueError := ankaCLI.EnsureVMTemplateExists(workerCtx, pluginCtx, workflowJob.AnkaTemplate, workflowJob.AnkaTemplateTag)
	if returnToQueueError != nil {
		logger.WarnContext(pluginCtx, "problem ensuring vm template exists on host", "err", returnToQueueError)
		failureChannel <- true // return to queue so another node can pick it up
		return
	}
	if noTemplateTagExistsError != nil {
		logger.ErrorContext(pluginCtx, "error ensuring vm template exists on host", "err", noTemplateTagExistsError)
		metricsData.IncrementTotalFailedRunsSinceStart()
		err := sendCancelWorkflowRun(pluginCtx, logger, workflowJob)
		if err != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
		}
		skipPrep = true
	}

	if pluginCtx.Err() != nil {
		logger.WarnContext(pluginCtx, "context canceled during vm template check")
		failureChannel <- true
		return
	}

	if !skipPrep {

		// Get runner registration token
		var runnerRegistration *github.RegistrationToken
		var response *github.Response
		var err error
		if isRepoSet {
			pluginCtx, runnerRegistration, response, err = executeGitHubClientFunction[github.RegistrationToken](pluginCtx, logger, func() (*github.RegistrationToken, *github.Response, error) {
				runnerRegistration, resp, err := githubClient.Actions.CreateRegistrationToken(context.Background(), ctxPlugin.Owner, ctxPlugin.Repo)
				return runnerRegistration, resp, err
			})
		} else {
			pluginCtx, runnerRegistration, response, err = executeGitHubClientFunction[github.RegistrationToken](pluginCtx, logger, func() (*github.RegistrationToken, *github.Response, error) {
				runnerRegistration, resp, err := githubClient.Actions.CreateOrganizationRegistrationToken(context.Background(), ctxPlugin.Owner)
				return runnerRegistration, resp, err
			})
		}
		if err != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "error creating registration token", "err", err, "response", response)
			failureChannel <- true
			return
		}
		if *runnerRegistration.Token == "" {
			logger.ErrorContext(pluginCtx, "registration token is empty; something wrong with github or your service token", "response", response)
			failureChannel <- true
			return
		}

		if pluginCtx.Err() != nil {
			logger.WarnContext(pluginCtx, "context canceled before ObtainAnkaVM")
			failureChannel <- true
			return
		}

		// Obtain Anka VM (and name)
		pluginCtx, vm, err := ankaCLI.ObtainAnkaVM(workerCtx, pluginCtx, workflowJob.AnkaTemplate)
		wrappedVM := map[string]interface{}{
			"type":    "anka.VM",
			"payload": vm,
		}
		wrappedVmJSON, wrappedVmErr := json.Marshal(wrappedVM)
		if wrappedVmErr != nil {
			logger.ErrorContext(pluginCtx, "error marshalling vm to json", "err", wrappedVmErr)
			ankaCLI.AnkaDelete(workerCtx, pluginCtx, vm)
			failureChannel <- true
			return
		}
		dbErr := databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+ctxPlugin.Name, wrappedVmJSON).Err()
		if dbErr != nil {
			logger.ErrorContext(pluginCtx, "error pushing vm data to database", "err", dbErr)
			failureChannel <- true
			return
		}
		if err != nil {
			// this is thrown, for example, when there is no capacity on the host
			// we must be sure to create the DB entry so cleanup happens properly
			logger.ErrorContext(pluginCtx, "error obtaining anka vm", "err", err)
			failureChannel <- true
			return
		}

		if pluginCtx.Err() != nil {
			logger.WarnContext(pluginCtx, "context canceled after ObtainAnkaVM")
			failureChannel <- true
			return
		}

		// Install runner
		globals := config.GetGlobalsFromContext(pluginCtx)
		logger.InfoContext(pluginCtx, "installing github runner inside of vm")
		installRunnerPath := globals.PluginsPath + "/handlers/github/install-runner.bash"
		registerRunnerPath := globals.PluginsPath + "/handlers/github/register-runner.bash"
		startRunnerPath := globals.PluginsPath + "/handlers/github/start-runner.bash"
		_, installRunnerErr := os.Stat(installRunnerPath)
		_, registerRunnerErr := os.Stat(registerRunnerPath)
		_, startRunnerErr := os.Stat(startRunnerPath)
		if installRunnerErr != nil || registerRunnerErr != nil || startRunnerErr != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "must include install-runner.bash, register-runner.bash, and start-runner.bash in "+globals.PluginsPath+"/handlers/github/", "err", err)
			err := sendCancelWorkflowRun(pluginCtx, logger, workflowJob)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
			}
			return
		}
		err = ankaCLI.AnkaCopy(pluginCtx,
			installRunnerPath,
			registerRunnerPath,
			startRunnerPath,
		)
		if err != nil {
			metricsData.IncrementTotalFailedRunsSinceStart()
			logger.ErrorContext(pluginCtx, "error executing anka copy", "err", err)
			failureChannel <- true
			return
		}

		select {
		case <-completedJobChannel:
			logger.InfoContext(pluginCtx, "completed job found before installing runner")
			completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before install runner")
			return
		default:
		}

		installRunnerErr = ankaCLI.AnkaRun(pluginCtx, "./install-runner.bash")
		if installRunnerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing install-runner.bash", "err", installRunnerErr)
			failureChannel <- true
			return
		}
		// Register runner
		select {
		case <-completedJobChannel:
			logger.InfoContext(pluginCtx, "completed job found before registering runner")
			completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before register runner")
			return
		default:
		}
		registerRunnerErr = ankaCLI.AnkaRun(pluginCtx,
			"./register-runner.bash",
			vm.Name, *runnerRegistration.Token, repositoryURL, strings.Join(workflowJob.Labels, ","),
		)
		if registerRunnerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing register-runner.bash", "err", registerRunnerErr)
			failureChannel <- true
			return
		}
		defer removeSelfHostedRunner(pluginCtx, *vm, workflowJob)
		// Install and Start runner
		select {
		case <-completedJobChannel:
			logger.InfoContext(pluginCtx, "completed job found before starting runner")
			completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before start runner")
			return
		default:
		}
		startRunnerErr = ankaCLI.AnkaRun(pluginCtx, "./start-runner.bash")
		if startRunnerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing start-runner.bash", "err", startRunnerErr)
			failureChannel <- true
			return
		}
		select {
		case <-completedJobChannel:
			logger.InfoContext(pluginCtx, "completed job found before jobCompleted checks")
			completedJobChannel <- github.WorkflowJobEvent{} // send true to the channel to stop the check for completed jobs goroutine
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before jobCompleted checks")
			return
		default:
		}

	} // skipPrep

	logger.InfoContext(pluginCtx, "watching for job completion")

	// Watch for job completion
	logCounter := 0
	for {
		select {
		case completedJobEvent := <-completedJobChannel:
			if *completedJobEvent.Action == "completed" {
				pluginCtx = logging.AppendCtx(pluginCtx, slog.String("conclusion", *completedJobEvent.WorkflowJob.Conclusion))
				logger.InfoContext(pluginCtx, "job completed",
					"job_id", completedJobEvent.WorkflowJob.ID,
					"conclusion", *completedJobEvent.WorkflowJob.Conclusion,
				)
				if *completedJobEvent.WorkflowJob.Conclusion == "success" {
					metricsData := metrics.GetMetricsDataFromContext(workerCtx)
					metricsData.IncrementTotalSuccessfulRunsSinceStart()
					metricsData.UpdatePlugin(pluginCtx, logger, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: ctxPlugin.Name,
						},
						LastSuccessfulRun:       time.Now(),
						LastSuccessfulRunJobUrl: *completedJobEvent.WorkflowJob.URL,
					})
				} else if *completedJobEvent.WorkflowJob.Conclusion == "failure" {
					metricsData := metrics.GetMetricsDataFromContext(workerCtx)
					metricsData.IncrementTotalFailedRunsSinceStart()
					metricsData.UpdatePlugin(pluginCtx, logger, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: ctxPlugin.Name,
						},
						LastFailedRun:       time.Now(),
						LastFailedRunJobUrl: *completedJobEvent.WorkflowJob.URL,
					})
				}
			} else if logCounter%2 == 0 {
				if pluginCtx.Err() != nil {
					logger.WarnContext(pluginCtx, "context canceled during job status check")
					return
				}
			}
			completedJobChannel <- github.WorkflowJobEvent{} // so cleanup can also see it as completed
			return
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled while watching for job completion")
			return
		default:
			time.Sleep(10 * time.Second)
			if logCounter%2 == 0 {
				logger.InfoContext(pluginCtx, "job still in progress", "job_id", workflowJob.JobID)
			}
			logCounter++
		}
		// pluginCtx, currentJob, response, err := ExecuteGitHubClientFunction[github.WorkflowJob](pluginCtx, logger, func() (*github.WorkflowJob, *github.Response, error) {
		// 	currentJob, resp, err := githubClient.Actions.GetWorkflowJobByID(context.Background(), service.Owner, service.Repo, workflowRunJob.JobID)
		// 	return currentJob, resp, err
		// })
		// if err != nil {
		// 	logger.ErrorContext(pluginCtx, "error executing githubClient.Actions.GetWorkflowJobByID", "err", err, "response", response)
		// 	return
		// }
		// if *currentJob.Status == "completed" {
		// 	jobCompleted = true
		// 	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("conclusion", *currentJob.Conclusion))
		// 	logger.InfoContext(pluginCtx, "job completed", "job_id", workflowRunJob.JobID)
		// 	if *currentJob.Conclusion == "success" {
		// 		metricsData := metrics.GetMetricsDataFromContext(workerCtx)
		// 		metricsData.IncrementTotalSuccessfulRunsSinceStart()
		// 		metricsData.UpdateService(pluginCtx, logger, metrics.Service{
		// 			Name:                    service.Name,
		// 			LastSuccessfulRun:       time.Now(),
		// 			LastSuccessfulRunJobUrl: workflowRunJob.JobURL,
		// 		})
		// 	} else if *currentJob.Conclusion == "failure" {
		// 		metricsData := metrics.GetMetricsDataFromContext(workerCtx)
		// 		metricsData.IncrementTotalFailedRunsSinceStart()
		// 		metricsData.UpdateService(pluginCtx, logger, metrics.Service{
		// 			Name:                service.Name,
		// 			LastFailedRun:       time.Now(),
		// 			LastFailedRunJobUrl: workflowRunJob.JobURL,
		// 		})
		// 	}
		// } else if logCounter%2 == 0 {
		// 	if pluginCtx.Err() != nil {
		// 		logger.WarnContext(pluginCtx, "context canceled during job status check")
		// 		return
		// 	}
		// 	logger.InfoContext(pluginCtx, "job still in progress", "job_id", workflowRunJob.JobID)
		// 	time.Sleep(5 * time.Second) // Wait before checking the job status again
		// }
	}
}

// removeSelfHostedRunner handles removing a registered runner if the registered runner was orphaned somehow
// it's extra safety should the runner not be registered with --ephemeral
func removeSelfHostedRunner(pluginCtx context.Context, vm anka.VM, workflow WorkflowRunJobDetail) {
	logger := logging.GetLoggerFromContext(pluginCtx)
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	githubClient := internalGithub.GetGitHubClientFromContext(pluginCtx)
	isRepoSet := config.GetIsRepoSetFromContext(pluginCtx)
	var runnersList *github.Runners
	var response *github.Response
	var err error
	if isRepoSet {
		pluginCtx, runnersList, response, err = executeGitHubClientFunction[github.Runners](pluginCtx, logger, func() (*github.Runners, *github.Response, error) {
			runnersList, resp, err := githubClient.Actions.ListRunners(context.Background(), ctxPlugin.Owner, ctxPlugin.Repo, &github.ListRunnersOptions{})
			return runnersList, resp, err
		})
	} else {
		pluginCtx, runnersList, response, err = executeGitHubClientFunction[github.Runners](pluginCtx, logger, func() (*github.Runners, *github.Response, error) {
			runnersList, resp, err := githubClient.Actions.ListOrganizationRunners(context.Background(), ctxPlugin.Owner, &github.ListRunnersOptions{})
			return runnersList, resp, err
		})
	}
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing githubClient.Actions.ListRunners", "err", err, "response", response)
		return
	}
	if len(runnersList.Runners) == 0 {
		logger.DebugContext(pluginCtx, "no runners found to delete (not an error)")
	} else {
		// found := false
		for _, runner := range runnersList.Runners {
			if *runner.Name == vm.Name {
				// found = true
				/*
					We have to cancel the workflow run before we can remove the runner.
					[11:12:53.736] ERROR: error executing githubClient.Actions.RemoveRunner {
					"ankaTemplate": "d792c6f6-198c-470f-9526-9c998efe7ab4",
					"ankaTemplateTag": "(using latest)",
					"err": "DELETE https://api.github.com/repos/veertuinc/anklet/actions/runners/142: 422 Bad request - Runner \"anklet-vm-\u003cuuid\u003e\" is still running a job\" []",
				*/
				err := sendCancelWorkflowRun(pluginCtx, logger, workflow)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
					return
				}
				if isRepoSet {
					pluginCtx, _, _, err = executeGitHubClientFunction[github.Response](pluginCtx, logger, func() (*github.Response, *github.Response, error) {
						response, err := githubClient.Actions.RemoveRunner(context.Background(), ctxPlugin.Owner, ctxPlugin.Repo, *runner.ID)
						return response, nil, err
					})
				} else {
					pluginCtx, _, _, err = executeGitHubClientFunction[github.Response](pluginCtx, logger, func() (*github.Response, *github.Response, error) {
						response, err := githubClient.Actions.RemoveOrganizationRunner(context.Background(), ctxPlugin.Owner, *runner.ID)
						return response, nil, err
					})
				}
				if err != nil {
					logger.ErrorContext(pluginCtx, "error executing githubClient.Actions.RemoveRunner", "err", err)
					return
				} else {
					logger.InfoContext(pluginCtx, "successfully removed runner")
				}
				break
			}
		}
		// if !found {
		// 	logger.InfoContext(pluginCtx, "no matching runner found")
		// }
	}
}
