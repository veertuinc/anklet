package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/redis/go-redis/v9"
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

var once sync.Once

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

func watchForJobCompletion(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
	mainInProgressQueueName string,
) (context.Context, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	pluginGlobals, err := internalGithub.GetPluginGlobalsFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	logCounter := 0
	alreadyLogged := false
	jobStatusCheckIntervalSeconds := 10
	firstLog := true
	for {
		select {
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		default:
			time.Sleep(time.Duration(jobStatusCheckIntervalSeconds) * time.Second)
			queuedJobJSON, err := databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, 0).Result()
			if err != nil {
				logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			queuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](queuedJobJSON)
			if err != nil || typeErr != nil {
				logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "queuedJobJSON", queuedJobJSON)
				return pluginCtx, err
			}
			logger.DebugContext(pluginCtx, "queuedJob", "queuedJob", queuedJob)
			if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
				logger.InfoContext(pluginCtx, "job completed",
					"job_id", queuedJob.WorkflowJob.ID,
					"conclusion", *queuedJob.WorkflowJob.Conclusion,
				)
				metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
				if err != nil {
					return pluginCtx, err
				}
				if *queuedJob.WorkflowJob.Conclusion == "success" || *queuedJob.WorkflowJob.Conclusion == "" {
					metricsData.IncrementTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx, logger)
					metricsData.UpdatePlugin(workerCtx, pluginCtx, logger, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: pluginConfig.Name,
						},
						LastSuccessfulRun:       time.Now(),
						LastSuccessfulRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
					})
				} else if *queuedJob.WorkflowJob.Conclusion == "failure" {
					metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
					metricsData.UpdatePlugin(workerCtx, pluginCtx, logger, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: pluginConfig.Name,
						},
						LastFailedRun:       time.Now(),
						LastFailedRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
					})
				}
				return pluginCtx, nil
			}

			mainInProgressQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			if mainInProgressQueueJobJSON != "" {
				if logCounter%2 == 0 {
					if firstLog {
						logger.InfoContext(pluginCtx, "job found registered runner and is now in progress", "job_id", *queuedJob.WorkflowJob.ID)
						firstLog = false
					} else {
						logger.InfoContext(pluginCtx, "job is still in progress", "job_id", *queuedJob.WorkflowJob.ID)
					}
				}
			} else {
				if !alreadyLogged {
					logger.InfoContext(pluginCtx, "job is waiting for registered runner", "job_id", *queuedJob.WorkflowJob.ID)
					alreadyLogged = true
				}
			}
			var registrationTimeoutSeconds int
			if pluginConfig.RegistrationTimeoutSeconds <= 0 {
				registrationTimeoutSeconds = 60
			} else {
				registrationTimeoutSeconds = pluginConfig.RegistrationTimeoutSeconds
			}
			if logCounter == int(registrationTimeoutSeconds/jobStatusCheckIntervalSeconds) {
				// after X seconds, check to see if the job has started in the in_progress queue
				// if not, then the runner registration failed
				if mainInProgressQueueJobJSON == "" {
					logger.ErrorContext(pluginCtx, "waiting for runner registration timed out, will retry")
					queuedJob.WorkflowJob.Conclusion = github.String("failure") // support removeSelfHostedRunner
					queuedJob.WorkflowJob.Status = github.String("completed")
					internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
					pluginGlobals.RetryChannel <- true
					return pluginCtx, nil
				}
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

func extractLabelValue(labels []string, prefix string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			result := strings.TrimPrefix(label, prefix)
			return result
		}
	}
	return ""
}

func sendCancelWorkflowRun(
	workerCtx context.Context,
	pluginCtx context.Context,
	logger *slog.Logger,
	queuedJob internalGithub.QueueJob,
	metricsData *metrics.MetricsDataLock,
) error {
	githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		return err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	cancelSent := false
	for {
		newPluginCtx, workflowRun, _, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.WorkflowRun, *github.Response, error) {
			workflowRun, resp, err := githubClient.Actions.GetWorkflowRunByID(context.Background(), pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.RunID)
			return workflowRun, resp, err
		})
		if err != nil {
			logger.ErrorContext(newPluginCtx, "error getting workflow run by ID", "err", err)
			return err
		}
		pluginCtx = newPluginCtx
		if *workflowRun.Status == "completed" ||
			(workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") ||
			cancelSent {
			metricsData.IncrementTotalCanceledRunsSinceStart(workerCtx, pluginCtx, logger)
			metricsData.UpdatePlugin(workerCtx, pluginCtx, logger, metrics.Plugin{
				PluginBase: &metrics.PluginBase{
					Name: pluginConfig.Name,
				},
				LastCanceledRun:       time.Now(),
				LastCanceledRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
			})
			break
		} else {
			logger.WarnContext(pluginCtx, "workflow run is still active... waiting for cancellation so we can clean up...", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
			if !cancelSent { // this has to happen here so that it doesn't error with "409 Cannot cancel a workflow run that is completed. " if the job is already cancelled
				newPluginCtx, cancelResponse, _, cancelErr := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.Response, *github.Response, error) {
					resp, err := githubClient.Actions.CancelWorkflowRunByID(context.Background(), pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.RunID)
					return resp, nil, err
				})
				// don't use cancelResponse.Response.StatusCode or else it'll error with SIGSEV
				if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
					logger.ErrorContext(newPluginCtx, "error executing githubClient.Actions.CancelWorkflowRunByID", "err", cancelErr, "response", cancelResponse)
					return cancelErr
				}
				pluginCtx = newPluginCtx
				cancelSent = true
				logger.WarnContext(pluginCtx, "sent cancel workflow run", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
			}
			time.Sleep(10 * time.Second)
		}
	}
	return nil
}

func checkForCompletedJobs(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
	pluginCompletedQueueName string,
	mainCompletedQueueName string,
	mainInProgressQueueName string,
) {
	randomInt := rand.Intn(100)
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting logger from context", "err", err)
		os.Exit(1)
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting globals from context", "err", err)
		os.Exit(1)
	}
	pluginGlobals, err := internalGithub.GetPluginGlobalsFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin global from context", "err", err)
		os.Exit(1)
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin from context", "err", err)
		os.Exit(1)
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		os.Exit(1)
	}
	defer func() {
		fmt.Println(pluginConfig.Name, " checkForCompletedJobs defer start")
		if pluginGlobals.CheckForCompletedJobsMutex != nil {
			pluginGlobals.CheckForCompletedJobsMutex.Unlock()
		}
		pluginGlobals.FirstCheckForCompletedJobsRan = true
		fmt.Println(pluginConfig.Name, " checkForCompletedJobs defer end")
	}()
	for {
		var updateDB bool = false
		// BE VERY CAREFUL when you use return here. You could orphan the job if you're not careful.
		pluginGlobals.CheckForCompletedJobsMutex.Lock()
		fmt.Println(pluginConfig.Name, " checkForCompletedJobs start loop", randomInt)
		// logger.DebugContext(pluginCtx, "checkForCompletedJobsMu locked")
		// do not use 'continue' in the loop or else the ranOnce won't happen
		// logging.DevContext(pluginCtx, "checkForCompletedJobs "+pluginConfig.Name+" | runOnce "+fmt.Sprint(runOnce))
		select {
		case <-pluginGlobals.PausedCancellationJobChannel:
			fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> pausedCancellationJobChannel", randomInt)
			return
		case <-pluginGlobals.RetryChannel:
			fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> retryChannel", randomInt)
			workerGlobals.ReturnToMainQueue <- true
			return
		case job := <-pluginGlobals.JobChannel:
			if job.Action == "finish" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> finished", randomInt)
				workerGlobals.ResetQueueTargetIndex()
				return
			}
		case <-pluginCtx.Done():
			logging.DevContext(pluginCtx, "checkForCompletedJobs "+pluginConfig.Name+" pluginCtx.Done()")
			return
		default:
		}

		// get the job ID
		existingJobString, err := databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, 0).Result()
		if err == redis.Nil || existingJobString == "" {
			fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> no job found in pluginQueue", randomInt)
		} else {
			fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job found in pluginQueue", randomInt)
			queuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](existingJobString)
			if err != nil || typeErr != nil {
				logger.ErrorContext(pluginCtx,
					"error unmarshalling job",
					"err", err,
					"typeErr", typeErr,
					"queuedJob", queuedJob,
				)
				return
			}

			// check if in_progress queue, so we can skip cleanup later on
			mainInProgressQueueJobJSON, err := internalGithub.InQueue(pluginCtx, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
				return
			}
			if mainInProgressQueueJobJSON != "" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in mainInProgressQueue", randomInt)
				mainInProgressQueueJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](mainInProgressQueueJobJSON)
				if err != nil || typeErr != nil {
					logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "mainInProgressQueueJobJSON", mainInProgressQueueJobJSON)
					return
				}
				if *queuedJob.WorkflowJob.Status != *mainInProgressQueueJob.WorkflowJob.Status { // prevent running the dbUpdate more than once if not needed
					updateDB = true
				}
				queuedJob.WorkflowJob.Status = github.String("in_progress")
			}

			var mainCompletedQueueJobJSON string
			var pluginCompletedQueueJobJSON string
			// check if there is already a completed job queued in the pluginCompletedQueue
			pluginCompletedQueueJobJSON, err = internalGithub.InQueue(pluginCtx, *queuedJob.WorkflowJob.ID, pluginCompletedQueueName)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
				return
			}
			if pluginCompletedQueueJobJSON != "" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in pluginCompletedQueue", randomInt)
			} else {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is not in pluginCompletedQueue", randomInt)
				// check if there is already a completed job queued in the mainCompletedQueue
				mainCompletedQueueJobJSON, err = internalGithub.InQueue(pluginCtx, *queuedJob.WorkflowJob.ID, mainCompletedQueueName)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error searching in queue", "error", err)
					return
				}
				if mainCompletedQueueJobJSON != "" {
					fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in mainCompletedQueue", randomInt)
				}
			}

			if pluginCompletedQueueJobJSON != "" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in pluginCompletedQueue, handling", randomInt)
				queuedJob.WorkflowJob.Status = github.String("completed")
				queuedJob.Action = "finish"
				select {
				case pluginGlobals.JobChannel <- queuedJob:
				default:
					// // remove the completed job we found
					// _, err = databaseContainer.Client.Del(pluginCtx, pluginCompletedQueueName).Result()
					// if err != nil {
					// 	logger.ErrorContext(pluginCtx, "error removing completedJob from "+pluginCompletedQueueName, "err", err)
					// 	return
					// }
					// fmt.Println("checkForCompletedJobs -> deleted existing job from "+pluginCompletedQueueName, "queuedJob", queuedJob)
				}
				updateDB = true
			}

			if mainCompletedQueueJobJSON != "" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in mainCompletedQueue, handling", randomInt)
				// remove the completed job we found
				success, err := databaseContainer.Client.LRem(pluginCtx, mainCompletedQueueName, 1, mainCompletedQueueJobJSON).Result()
				if err != nil {
					logger.ErrorContext(pluginCtx,
						"error removing completedJob from "+mainCompletedQueueName,
						"err", err,
						"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
					)
					return
				}
				if success == 1 {
					fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> removed job from mainCompletedQueue", randomInt)
				} else { // handle if another host removed the job
					logger.ErrorContext(
						pluginCtx,
						"non-successful removal of completedJob from "+mainCompletedQueueName,
						"err", err,
						"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
					)
					return
				}

				completedQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](mainCompletedQueueJobJSON)
				if err != nil || typeErr != nil {
					logger.ErrorContext(pluginCtx,
						"error unmarshalling job",
						"err", err,
						"typeErr", typeErr,
						"queuedJob", queuedJob,
					)
					return
				}
				queuedJob.WorkflowJob.Status = completedQueuedJob.WorkflowJob.Status
				queuedJob.WorkflowJob.Conclusion = completedQueuedJob.WorkflowJob.Conclusion
				// delete the existing service task
				// _, err = databaseContainer.Client.Del(pluginCtx, serviceQueueDatabaseKeyName).Result()
				// if err != nil {
				// 	logger.ErrorContext(pluginCtx, "error deleting all objects from "+serviceQueueDatabaseKeyName, "err", err)
				// 	return
				// }
				// add a task for the completed job so we know the clean up
				_, err = databaseContainer.Client.LPush(pluginCtx, pluginCompletedQueueName, mainCompletedQueueJobJSON).Result()
				if err != nil {
					logger.ErrorContext(pluginCtx, "error inserting completed job into list", "err", err)
					return
				}
				pluginGlobals.JobChannel <- queuedJob
				updateDB = true
			}

			// if in progress, but no completed job found, we need to check the status of the job in github's API
			if !pluginGlobals.FirstCheckForCompletedJobsRan &&
				mainCompletedQueueJobJSON == "" && pluginCompletedQueueJobJSON == "" &&
				mainInProgressQueueJobJSON != "" {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in mainInProgressQueue, checking status from API", randomInt)
				githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting github client from context", "err", err)
				}
				pluginConfig, err := config.GetPluginFromContext(pluginCtx)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting plugin from context", "err", err)
				}
				pluginCtx, workflowJob, _, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.WorkflowJob, *github.Response, error) {
					workflowJob, response, err := githubClient.Actions.GetWorkflowJobByID(pluginCtx, pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.ID)
					return workflowJob, response, err
				})
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting workflow run", "err", err)
					return
				}
				logger.DebugContext(pluginCtx, "workflowJob from API", "workflowJob", workflowJob)
				// logger.DebugContext(pluginCtx, "response", "response", response)
				// Handle each workflow job status with a log message
				// completed = we clean up everything
				// failed = we clean up everything
				// queued = let it run, don't do anything
				// running = let it run, don't do anything
				if workflowJob.Status != nil {
					status := *workflowJob.Status
					switch status {
					case "completed":
						logger.InfoContext(pluginCtx, "workflow job is completed")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "action_required":
						logger.InfoContext(pluginCtx, "workflow job requires action")
						queuedJob.WorkflowJob.Conclusion = github.String("failure")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "cancelled":
						logger.InfoContext(pluginCtx, "workflow job was cancelled")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "failure":
						logger.InfoContext(pluginCtx, "workflow job failed")
						queuedJob.WorkflowJob.Conclusion = github.String("failure")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "neutral":
						logger.InfoContext(pluginCtx, "workflow job ended with neutral status")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "skipped":
						logger.InfoContext(pluginCtx, "workflow job was skipped")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "stale":
						logger.InfoContext(pluginCtx, "workflow job is stale")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "success":
						logger.InfoContext(pluginCtx, "workflow job succeeded")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "timed_out":
						logger.InfoContext(pluginCtx, "workflow job timed out")
						queuedJob.WorkflowJob.Conclusion = github.String("failure")
						queuedJob.WorkflowJob.Status = github.String("completed")
					case "in_progress":
						logger.InfoContext(pluginCtx, "workflow job is in progress")
						queuedJob.WorkflowJob.Status = github.String("in_progress")
					case "queued":
						logger.InfoContext(pluginCtx, "workflow job is queued")
						queuedJob.WorkflowJob.Status = github.String("queued")
					case "requested":
						logger.InfoContext(pluginCtx, "workflow job was requested")
						queuedJob.WorkflowJob.Status = github.String("queued")
					case "waiting":
						logger.InfoContext(pluginCtx, "workflow job is waiting")
						queuedJob.WorkflowJob.Status = github.String("in_progress")
					case "pending":
						logger.InfoContext(pluginCtx, "workflow job is pending")
						queuedJob.WorkflowJob.Status = github.String("in_progress")
					default:
						logger.InfoContext(pluginCtx, "workflow job has unknown status", "status", status)
					}
				} else {
					logger.WarnContext(pluginCtx, "workflow job status is nil")
				}
				if queuedJob.WorkflowJob.Status != nil &&
					(*queuedJob.WorkflowJob.Status == "completed" || *queuedJob.WorkflowJob.Status == "failed") {
					// add a task for the completed job so we know the clean up
					queuedJobJSON, err := json.Marshal(queuedJob)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error marshalling queued job", "err", err)
						return
					}
					_, err = databaseContainer.Client.LPush(pluginCtx, pluginCompletedQueueName, queuedJobJSON).Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error inserting completed job into list", "err", err)
						return
					}
				}
				pluginGlobals.JobChannel <- queuedJob
				updateDB = true
			}

			// update the job in the database so we can get the new status for subsequent steps
			if updateDB {
				fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> updating job in database", *queuedJob.WorkflowJob.Status)
				internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
			}
		}

		pluginGlobals.FirstCheckForCompletedJobsRan = true
		if pluginGlobals.CheckForCompletedJobsMutex != nil {
			pluginGlobals.CheckForCompletedJobsMutex.Unlock()
		}
		fmt.Println(pluginConfig.Name, " checkForCompletedJobs end loop", randomInt)
		time.Sleep(3 * time.Second)
	}
}

// cleanup will pop off the last item from the list and, based on its type, perform the appropriate cleanup action
// this assumes the plugin code created a list item to represent the thing to clean up
func cleanup(
	workerCtx context.Context,
	pluginCtx context.Context,
	mainQueueName string,
	pluginQueueName string,
	mainInProgressQueueName string,
	pluginCompletedQueueName string,
	pausedQueueName string,
	queuedJobFromPausedQueue *bool,
	onStartRun bool,
) {
	fmt.Println(pluginQueueName, "cleanup | plugin cleanup started")
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting logger from context", "err", err)
		os.Exit(1)
	}
	pluginGlobals, err := internalGithub.GetPluginGlobalsFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin global from context", "err", err)
		return
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin from context", "err", err)
		return
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting worker global from context", "err", err)
		return
	}

	fmt.Println(pluginConfig.Name, "cleanup | locking plugin cleanup mutex")
	pluginGlobals.CleanupMutex.Lock()
	fmt.Println(pluginConfig.Name, "cleanup | locked plugin cleanup mutex 2")

	defer func() {
		if pluginGlobals.CleanupMutex != nil {
			fmt.Println(pluginConfig.Name, "cleanup | unlocking plugin cleanup mutex")
			pluginGlobals.CleanupMutex.Unlock()
		}
		if !onStartRun {
			fmt.Println(pluginConfig.Name, "cleanup | unlocking plugin preparing state")
			if workerGlobals.IsAPluginPreparingState() == pluginConfig.Name {
				workerGlobals.UnsetAPluginIsPreparing()
			}
		}
	}()

	// create an idependent copy of the pluginCtx so we can do cleanup even if pluginCtx got "context canceled"
	cleanupContext := context.Background()

	fmt.Println(pluginConfig.Name, "cleanup | HERE 1")

	select {
	case <-pluginGlobals.PausedCancellationJobChannel: // no cleanup necessary, it was picked up by another host
		logger.DebugContext(pluginCtx, "cleanup pausedCancellationJobChannel")
		return
	default:
	}

	fmt.Println(pluginConfig.Name, "cleanup | HERE 2")

	serviceDatabase, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("database"), serviceDatabase)
	cleanupContext, cancel := context.WithCancel(cleanupContext)
	defer func() {
		cancel()
	}()
	databaseContainer, err := database.GetDatabaseFromContext(cleanupContext)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return
	}

	fmt.Println(pluginConfig.Name, "cleanup | HERE 3")

	// get the original job with the latest status and check if it's running
	originalJobJSON, err := databaseContainer.Client.LIndex(cleanupContext, pluginQueueName, 0).Result()
	if err != nil && err != redis.Nil {
		logger.ErrorContext(pluginCtx, "error getting job from the list", "err", err)
		return
	}
	if err == redis.Nil {
		fmt.Println(pluginConfig.Name, "cleanup | HERE 4")
		return // nothing to clean up
	}
	originalQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](originalJobJSON)
	if err != nil || typeErr != nil {
		logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "originalJobJSON", originalJobJSON)
		return
	}

	// if the job is running, we don't need to clean it up yet
	if originalQueuedJob.WorkflowJob.Status != nil && *originalQueuedJob.WorkflowJob.Status == "in_progress" {
		logger.DebugContext(pluginCtx, "job is still running; skipping cleanup")
		return
	}

	logger.DebugContext(pluginCtx, "starting cleanup loop")
	for {
		var queuedJob internalGithub.QueueJob
		var typeErr error
		var cleaningJobJSON string

		// get the cleaning job from the cleaning list
		cleaningJobJSON, err = databaseContainer.Client.LIndex(cleanupContext, pluginQueueName+"/cleaning", 0).Result()
		if err != nil && err != redis.Nil {
			logger.ErrorContext(pluginCtx, "error getting job from the list", "err", err)
			return
		}
		if cleaningJobJSON == "" {
			if onStartRun {
				return
			}
			// if nothing in cleaning already, pop the job from the list and push it to the cleaning list
			cleaningJobJSON, err = databaseContainer.Client.RPopLPush(
				cleanupContext,
				pluginQueueName,
				pluginQueueName+"/cleaning",
			).Result()
			if err == redis.Nil {
				// logger.DebugContext(pluginCtx, "no job to clean up from "+pluginQueueName)
				return // nothing to clean up
			} else if err != nil {
				logger.ErrorContext(pluginCtx, "error popping job from the list", "err", err)
				return
			}
		}

		queuedJob, err, typeErr = database.Unwrap[internalGithub.QueueJob](cleaningJobJSON)
		if err != nil || typeErr != nil {
			if err == redis.Nil {
				logger.DebugContext(pluginCtx, "no job to clean up from "+pluginQueueName)
				return // nothing to clean up
			}
			logger.ErrorContext(pluginCtx,
				"error unmarshalling job",
				"err", err,
				"typeErr", typeErr,
				"cleaningJobJSON", cleaningJobJSON,
			)
			return
		}

		logger.DebugContext(pluginCtx, "cleaning queuedJob", "queuedJob", queuedJob)
		switch queuedJob.Type {
		case "anka.VM":
			logger.DebugContext(pluginCtx, "cleanup | anka.VM | queuedJob", "queuedJob", queuedJob)
			ankaCLI, err := internalAnka.GetAnkaCLIFromContext(pluginCtx)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error getting ankaCLI from context", "err", err)
				return
			}
			if strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEBUG" || strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEV" {
				err = ankaCLI.AnkaCopyOutOfVM(pluginCtx, queuedJob.AnkaVM.Name, "/Users/anka/actions-runner/_diag", "/tmp/"+queuedJob.AnkaVM.Name)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error copying actions runner out of vm", "err", err)
				}
			}
			ankaCLI.AnkaDelete(workerCtx, pluginCtx, queuedJob.AnkaVM.Name)
			databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			continue // required to keep processing tasks in the db list
		case "WorkflowJobPayload": // MUST COME LAST
			logger.DebugContext(pluginCtx, "cleanup | WorkflowJobPayload | queuedJob", "queuedJob", queuedJob)
			// delete the in_progress queue's index that matches the wrkflowJobID
			err = internalGithub.DeleteFromQueue(cleanupContext, logger, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error deleting from in_progress queue", "err", err)
			}

			if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
				fmt.Println(pluginConfig.Name, "cleanup | WorkflowJobPayload | status is completed, so clean everything up", "job.WorkflowJob.Status", *queuedJob.WorkflowJob.Status)
				databaseContainer.Client.Del(cleanupContext, pluginCompletedQueueName)
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
				break
			}
			// return it to the queue if the job isn't completed yet
			// if we don't, we could suffer from a situation where a completed job comes in and is orphaned
			select {
			case <-pluginGlobals.PausedCancellationJobChannel:
				fmt.Println(pluginConfig.Name, "cleanup | WorkflowJobPayload | PausedCancellationJobChannel")
				// if the job was paused, we need to remove it
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
				return
			case <-workerGlobals.ReturnToMainQueue:
				fmt.Println(pluginConfig.Name, "cleanup | WorkflowJobPayload | workerGlobals.ReturnToMainQueue")

				var targetQueueName string
				if *queuedJobFromPausedQueue {
					targetQueueName = pausedQueueName
				} else {
					targetQueueName = mainQueueName
				}

				logger.WarnContext(pluginCtx, "pushing job from "+pluginQueueName+"/cleaning back to "+targetQueueName)
				_, err := databaseContainer.Client.RPopLPush(cleanupContext, pluginQueueName+"/cleaning", targetQueueName).Result()
				if err != nil {
					logger.ErrorContext(pluginCtx, "error pushing job back to queued", "err", err)
					return
				}
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			default:
				if queuedJob.WorkflowJob.Status != nil &&
					(*queuedJob.WorkflowJob.Status == "completed" || *queuedJob.WorkflowJob.Status == "failed") { // don't send it back to the queue if the job is completed
					databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
					return
				}

				logger.WarnContext(pluginCtx, "pushing job back to "+pluginQueueName)
				_, err := databaseContainer.Client.RPopLPush(cleanupContext, pluginQueueName+"/cleaning", pluginQueueName).Result()
				if err != nil {
					logger.ErrorContext(pluginCtx, "error pushing job back to queued", "err", err)
					return
				}
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			}
		default:
			logger.ErrorContext(pluginCtx, "unknown job type", "job", queuedJob)
			return
		}
		return // don't delete the queued/servicename
	}
}

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
	fmt.Println(pluginConfig.Name, "Run START===================")

	isRepoSet, err := config.GetIsRepoSetFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	metricsData.AddPlugin(metrics.Plugin{
		PluginBase: &metrics.PluginBase{
			Name:        pluginConfig.Name,
			PluginName:  pluginConfig.Plugin,
			RepoName:    pluginConfig.Repo,
			OwnerName:   pluginConfig.Owner,
			Status:      "idle",
			StatusSince: time.Now(),
		},
	})

	once.Do(func() {
		metrics.ExportMetricsToDB(pluginCtx, logger)
	})

	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	// Block other plugins from running until we're done preparing the VM so that
	// VmHasEnoughResources has a running VM to compare resources against
	workerGlobals.SetAPluginIsPreparing(pluginConfig.Name)

	// must come after first cleanup
	pluginGlobals := internalGithub.PluginGlobals{
		FirstCheckForCompletedJobsRan: false,
		CheckForCompletedJobsMutex:    &sync.Mutex{},
		RetryChannel:                  make(chan bool, 1),
		CleanupMutex:                  &sync.Mutex{},
		JobChannel:                    make(chan internalGithub.QueueJob, 1),
	}
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("pluginglobals"), &pluginGlobals)

	configFileName, err := config.GetConfigFileNameFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	if pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in " + configFileName + ":plugins:" + pluginConfig.Name + "<token/private_key>")
	}
	if pluginConfig.PrivateKey != "" && (pluginConfig.AppID == 0 || pluginConfig.InstallationID == 0) {
		return pluginCtx, fmt.Errorf("private_key, app_id, and installation_id must all be set in " + configFileName + ":plugins:" + pluginConfig.Name + "<token/private_key>")
	}
	if strings.HasPrefix(pluginConfig.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: " + err.Error())
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}
	if pluginConfig.Owner == "" {
		return pluginCtx, fmt.Errorf("owner is not set in " + configFileName + ":plugins:" + pluginConfig.Name + "<owner>")
	}
	// if pluginConfig.Repo == "" {
	// 	logging.Panic(workerCtx, pluginCtx, "repo is not set in anklet.yaml:plugins:"+pluginConfig.Name+":repo")
	// }

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
		return pluginCtx, fmt.Errorf("error authenticating github client")
	}
	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	var repositoryURL string
	if isRepoSet {
		pluginCtx = logging.AppendCtx(pluginCtx, slog.String("repo", pluginConfig.Repo))
		repositoryURL = fmt.Sprintf("https://github.com/%s/%s", pluginConfig.Owner, pluginConfig.Repo)
	} else {
		repositoryURL = fmt.Sprintf("https://github.com/%s", pluginConfig.Owner)
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("owner", pluginConfig.Owner))

	queuedJobFromPausedQueue := false

	// wait group so we can wait for the goroutine to finish before exiting the service
	var wg sync.WaitGroup
	wg.Add(1)

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		// logger.ErrorContext(pluginCtx, "error getting database from context", "err", err)
		return pluginCtx, fmt.Errorf("error getting database from context: %s", err.Error())
	}

	mainQueueName := "anklet/jobs/github/queued/" + pluginConfig.Owner
	mainCompletedQueueName := "anklet/jobs/github/completed/" + pluginConfig.Owner
	pluginQueueName := "anklet/jobs/github/queued/" + pluginConfig.Owner + "/" + pluginConfig.Name
	pluginCompletedQueueName := "anklet/jobs/github/completed/" + pluginConfig.Owner + "/" + pluginConfig.Name
	mainInProgressQueueName := "anklet/jobs/github/in_progress/" + pluginConfig.Owner
	pausedQueueName := "anklet/jobs/github/paused/" + pluginConfig.Owner

	defer func() {
		wg.Wait()
		cleanup(
			workerCtx,
			pluginCtx,
			mainQueueName,
			pluginQueueName,
			mainInProgressQueueName,
			pluginCompletedQueueName,
			pausedQueueName,
			&queuedJobFromPausedQueue,
			false,
		)
		fmt.Println(pluginConfig.Name, "cleanup done")
		close(pluginGlobals.JobChannel)
		fmt.Println(pluginConfig.Name, "pluginGlobals.JobChannel closed")
	}()

	// check constantly for a cancelled/completed webhook to be received for our job
	go func() {
		checkForCompletedJobs(
			workerCtx,
			pluginCtx,
			pluginQueueName,
			pluginCompletedQueueName,
			mainCompletedQueueName,
			mainInProgressQueueName,
		)
		wg.Done()
	}()
	for !pluginGlobals.FirstCheckForCompletedJobsRan {
		logger.DebugContext(pluginCtx, "waiting for first run checks to complete")
		if pluginCtx.Err() != nil {
			return pluginCtx, fmt.Errorf("context canceled while waiting for first checkForCompletedJobs to run")
		}
		time.Sleep(1 * time.Second)
	}
	select {
	case jobFromJobChannel := <-pluginGlobals.JobChannel:
		// return if completed so the cleanup doesn't run twice
		if *jobFromJobChannel.WorkflowJob.Status == "completed" || *jobFromJobChannel.WorkflowJob.Status == "failed" {
			logger.InfoContext(pluginCtx, "completed job found at start")
			return pluginCtx, nil
		}
		if *jobFromJobChannel.WorkflowJob.Status == "in_progress" { // TODO: make this the same as the loop later on
			if workerGlobals.IsAPluginPreparingState() == pluginConfig.Name {
				workerGlobals.UnsetAPluginIsPreparing()
			}
			pluginCtx, err = watchForJobCompletion(
				workerCtx,
				pluginCtx,
				pluginQueueName,
				mainInProgressQueueName,
			)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error watching for job completion", "err", err)
				return pluginCtx, err
			}
			return pluginCtx, nil
		}
	default:
		logger.InfoContext(pluginCtx, "no existing jobs found on startup")
	}

	// finalize cleanup if the service crashed mid-cleanup
	cleanup(
		workerCtx,
		pluginCtx,
		mainQueueName,
		pluginQueueName,
		mainInProgressQueueName,
		pluginCompletedQueueName,
		pausedQueueName,
		&queuedJobFromPausedQueue,
		true,
	)
	select {
	case runningJob := <-pluginGlobals.JobChannel:
		fmt.Println("after first cleanup status", runningJob.WorkflowJob.Status)
		if *runningJob.WorkflowJob.Status == "completed" || *runningJob.WorkflowJob.Status == "failed" {
			logger.InfoContext(pluginCtx, "completed or failed job found at start")
			pluginGlobals.JobChannel <- runningJob
			return pluginCtx, nil
		}
	case <-pluginCtx.Done():
		logger.WarnContext(pluginCtx, "context canceled before completed job found")
		return pluginCtx, nil
	default:
	}

	hostHasVmCapacity := internalAnka.HostHasVmCapacity(pluginCtx)
	if !hostHasVmCapacity {
		logger.WarnContext(pluginCtx, "host does not have vm capacity")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	}

	// // check if another plugin is already running preparation phase
	// for !workerGlobals.IsMyTurnForPrepLock(pluginConfig.Name) {
	// 	logger.DebugContext(pluginCtx, "not this plugin's turn for prep, waiting...")
	// 	time.Sleep(2 * time.Second)
	// 	if pluginCtx.Err() != nil {
	// 		return pluginCtx, fmt.Errorf("context canceled while waiting for prep lock")
	// 	}
	// }

	logger.InfoContext(pluginCtx, "checking for jobs....")

	// alreadyNexted := false
	// pluginGlobals.AlreadyNextedPrepLock = &alreadyNexted // important so the next cleanup doesn't advance the prep lock/index

	var queuedJobString string
	var queuedJob internalGithub.QueueJob

	// allow picking up where we left off
	// always get -1 so we get the eldest job in the queue
	queuedJobString, err = databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, -1).Result()
	if err != nil && err != redis.Nil {
		// logger.ErrorContext(pluginCtx, "error getting last object from anklet/jobs/github/queued/"+pluginConfig.Owner+"/"+pluginConfig.Name, "err", err)
		return pluginCtx, fmt.Errorf("error getting last object from " + pluginQueueName)
	}
	if queuedJobString != "" {
		var typeErr error
		queuedJob, err, typeErr = database.Unwrap[internalGithub.QueueJob](queuedJobString)
		if err != nil || typeErr != nil {
			return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
		}
	} else {
		// if we haven't done anything before, get something from the main queue

		// Check if there are any paused jobs we can pick up
		// paused jobs take priority since they were higher in the queue and something picked them up already
		for {
			// Check the length of the paused queue
			pausedQueueLength, err := databaseContainer.Client.LLen(pluginCtx, pausedQueueName).Result()
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting paused queue length: %s", err.Error())
			}
			if pausedQueueLength == 0 {
				break
			}
			pausedQueuedJobString, err := internalGithub.PopJobOffQueue(pluginCtx, pausedQueueName, workerGlobals.QueueTargetIndex)
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting paused jobs: %s", err.Error())
			}
			if pausedQueuedJobString == "" {
				workerGlobals.ResetQueueTargetIndex()
				break
			}
			// Process each paused job and find one we can run
			var typeErr error
			pausedQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
			if err != nil || typeErr != nil {
				return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
			}
			err = internalAnka.VmHasEnoughHostResources(pluginCtx, pausedQueuedJob.AnkaVM)
			if err != nil {
				workerGlobals.IncrementQueueTargetIndex()
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}
			err = internalAnka.VmHasEnoughResources(pluginCtx, pausedQueuedJob.AnkaVM)
			if err != nil {
				workerGlobals.IncrementQueueTargetIndex()
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}

			// pull the workflow job from the currently paused host's queue and put it in the current queue instead
			originalHostJob, err := internalGithub.PopJobOffQueue(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/"+pausedQueuedJob.PausedOn, 0)
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting job from paused host's queue: %s", err.Error())
			}
			queuedJobString = originalHostJob
			break
		}

		// If not paused job to get, get a job from the main queue
		if queuedJobString == "" {
			queuedJobString, err = internalGithub.PopJobOffQueue(pluginCtx, mainQueueName, workerGlobals.QueueTargetIndex)
			if err != nil {
				metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
				fmt.Printf("resetting queue target index 1\n")
				workerGlobals.ResetQueueTargetIndex()
				return pluginCtx, fmt.Errorf("error getting queued jobs: %s", err.Error())
			}
			if queuedJobString == "" { // no queued jobs
				logger.DebugContext(pluginCtx, "no queued jobs found")
				// free up other plugins to run
				if workerGlobals.IsAPluginPreparingState() == pluginConfig.Name {
					workerGlobals.UnsetAPluginIsPreparing()
				}
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
				return pluginCtx, nil
			}
			var typeErr error
			queuedJob, err, typeErr = database.Unwrap[internalGithub.QueueJob](queuedJobString)
			if err != nil || typeErr != nil {
				return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
			}
		}

		databaseContainer.Client.RPush(pluginCtx, pluginQueueName, queuedJobString)

		// queuedJobString, err = databaseContainer.Client.LIndex(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner, globals.QueueIndex).Result()
		// if err == nil {
		// 	// Remove the job at the specific index
		// 	_, err = databaseContainer.Client.LSet(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner, globals.QueueIndex, "TO_BE_REMOVED").Result()
		// 	if err != nil {
		// 		return pluginCtx, fmt.Errorf("error setting job at index to TO_BE_REMOVED: %s", err.Error())
		// 	}
		// 	_, err = databaseContainer.Client.LRem(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner, 1, "TO_BE_REMOVED").Result()
		// 	if err != nil {
		// 		logger.ErrorContext(pluginCtx, "error removing job from main queue", "err", err)
		// 		return pluginCtx, fmt.Errorf("error removing job from main queue: %s", err.Error())
		// 	}
		// }
		// if err == redis.Nil {
		// 	logger.DebugContext(pluginCtx, "no queued jobs found")
		// 	globals.ResetQueueIndex()
		// 	completedJobChannel <- internalGithub.QueueJob{} // send true to the channel to stop the check for completed jobs goroutine
		// 	return pluginCtx, nil
		// }
		// if err != nil {
		// 	logger.ErrorContext(pluginCtx, "error getting queued jobs", "err", err)
		// 	metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
		// 	globals.ResetQueueIndex()
		// 	return pluginCtx, fmt.Errorf("error getting queued jobs: %s", err.Error())
		// }
		// databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/"+pluginConfig.Name, queuedJobString)
	}

	if !isRepoSet && queuedJob.Repository.Name != nil {
		pluginCtx = logging.AppendCtx(pluginCtx, slog.String("repo", *queuedJob.Repository.Name))
	}
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobID", *queuedJob.WorkflowJob.ID))
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowJobName", *queuedJob.WorkflowJob.Name))
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobRunID", *queuedJob.WorkflowJob.RunID))
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowName", *queuedJob.WorkflowJob.WorkflowName))
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.String("jobURL", *queuedJob.WorkflowJob.HTMLURL))
	logger.InfoContext(
		pluginCtx,
		"queued job found",
		"queuedJob", queuedJob,
	)

	// now that the job is in the plugin's queue,check if the job is already completed so we don't orphan if there is
	// a job in anklet/jobs/github/queued and also a anklet/jobs/github/completed
	pluginGlobals.FirstCheckForCompletedJobsRan = false
	for !pluginGlobals.FirstCheckForCompletedJobsRan {
		fmt.Println(pluginConfig.Name, "waiting for checkForCompletedJobs to run once more before we proceed")
		time.Sleep(1 * time.Second)
	}
	select {
	case job := <-pluginGlobals.JobChannel:
		if *job.WorkflowJob.Status == "completed" || *job.WorkflowJob.Status == "failed" {
			logger.InfoContext(pluginCtx, "job found by checkForCompletedJobs (at start of Run)")
			pluginGlobals.JobChannel <- job // send true to the channel to stop the check for completed jobs goroutine
			return pluginCtx, nil
		}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logger.WarnContext(pluginCtx, "context canceled before completed job found")
		return pluginCtx, nil
	default:
	}

	// get anka template
	ankaTemplate := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template:")
	if ankaTemplate == "" {
		// logger.WarnContext(pluginCtx, "warning: unable to find Anka Template specified in labels - skipping")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, fmt.Errorf("warning: unable to find Anka Template specified in labels - skipping")
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("ankaTemplate", ankaTemplate))
	ankaTemplateTag := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template-tag:")
	if ankaTemplateTag == "" {
		ankaTemplateTag = "(using latest)"
	}
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("ankaTemplateTag", ankaTemplateTag))

	// get anka CLI
	ankaCLI, err := internalAnka.GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, err
	}

	logger.InfoContext(pluginCtx, "handling anka workflow run job")
	metricsData.SetStatus(pluginCtx, logger, "in_progress")

	if queuedJob.AnkaVM.CPUCount == 0 || queuedJob.AnkaVM.MEMBytes == 0 { // no need to get VM info if we already have it

		// Check if host has enough resources for the template
		isRegistryRunning, err := ankaCLI.AnkaRegistryRunning(pluginCtx)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error checking if registry is running", "err", err)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil
		}
		if isRegistryRunning {
			ankaRegistryVMInfo, err := internalAnka.GetAnkaRegistryVmInfo(pluginCtx, ankaTemplate, ankaTemplateTag)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error getting anka registry vm info", "err", err)
				pluginGlobals.RetryChannel <- true
				return pluginCtx, nil
			}
			queuedJob.AnkaVM = *ankaRegistryVMInfo
		} else {
			logger.ErrorContext(pluginCtx, "anka registry is not running, checking if template is already pulled")
			ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, ankaTemplate)
			if err != nil { // doesn't exist locally
				logger.ErrorContext(pluginCtx, "template doesn't exist locally", "err", err)
				workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
				pluginGlobals.RetryChannel <- true
				return pluginCtx, nil
			}
			if ankaShowOutput.Tag != ankaTemplateTag { // exists locally but doesn't match the tag specified in the labels
				logger.ErrorContext(pluginCtx, "current anka template tag on the host doesn't match the tag specified in the labels", "ankaShowOutput.Tag", ankaShowOutput.Tag)
				workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
				pluginGlobals.RetryChannel <- true
				return pluginCtx, nil
			}
			// Determine CPU and MEM from the template and tag
			templateInfo, err := internalAnka.GetAnkaVmInfo(pluginCtx, ankaTemplate)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error getting vm info", "err", err)
				pluginGlobals.RetryChannel <- true
				return pluginCtx, fmt.Errorf("error getting vm info: %s", err.Error())
			}
			queuedJob.AnkaVM = *templateInfo
		}

		internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
	}
	logger.DebugContext(pluginCtx, "vmInfo", "queuedJob.AnkaVM", queuedJob.AnkaVM)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

	if queuedJob.Action != "paused" { // no need to do this, we already did it when we got the paused job
		// check if the host has enough resources to run this VM
		err = internalAnka.VmHasEnoughHostResources(pluginCtx, queuedJob.AnkaVM)
		if err != nil {
			logger.WarnContext(pluginCtx, "host does not have enough resources to run vm")
			workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil
		}

		// check if, compared to other VMs potentially running, we have enough resources to run this VM
		err = internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM)
		if err != nil {
			// push to proper queue so other hosts with the same specs can pick it up if available
			queuedJob.PausedOn = pluginConfig.Name
			queuedJob.Action = "paused"
			queuedJobJSON, err := json.Marshal(queuedJob)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error marshalling queued job", "err", err)
				pluginGlobals.RetryChannel <- true
				return pluginCtx, nil
			}
			databaseContainer.Client.RPush(pluginCtx, pausedQueueName, queuedJobJSON)
			logger.WarnContext(pluginCtx, "cannot run vm yet, waiting for enough resources to be available...")
			for {
				if pluginCtx.Err() != nil {
					pluginGlobals.RetryChannel <- true
					return pluginCtx, fmt.Errorf("context canceled while waiting for resources")
				}
				time.Sleep(5 * time.Second)
				logger.WarnContext(pluginCtx, "waiting for enough resources to be available...")
				// check if the job was picked up by another host
				hasJob, err := internalGithub.CheckIfQueueHasJob(pluginCtx, pluginQueueName)
				if err != nil {
					pluginGlobals.RetryChannel <- true
					return pluginCtx, fmt.Errorf("error checking if queue has job: %s", err.Error())
				}
				if !hasJob { // some other host has taken the job from this host
					logger.WarnContext(pluginCtx, "job was picked up by another host")
					pluginGlobals.PausedCancellationJobChannel <- queuedJob
					return pluginCtx, nil
				}
				// If there is still a queued job, check if the host has enough resources to run it
				err = internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM)
				if err != nil {
					logger.WarnContext(pluginCtx, err.Error())
					continue
				}
				break
			}
		}
	}

	logger.InfoContext(pluginCtx, "vm has enough resources to run; starting runner")

	skipPrep := false // allows us to wait for the cancellation we sent to be received so we can clean up properly

	// See if VM Template existing already
	if !pluginConfig.SkipPull {
		noTemplateTagExistsError, templateExistsError := ankaCLI.EnsureVMTemplateExists(workerCtx, pluginCtx, ankaTemplate, ankaTemplateTag)
		if templateExistsError != nil {
			// DO NOT RETURN AN ERROR TO MAIN. It will cause the other job on this node to be cancelled.
			logger.WarnContext(pluginCtx, "problem ensuring vm template exists on host", "err", templateExistsError)
			pluginGlobals.RetryChannel <- true // return to queue so another node can pick it up
			return pluginCtx, nil
		}
		if noTemplateTagExistsError != nil {
			logger.ErrorContext(pluginCtx, "error ensuring vm template exists on host", "err", noTemplateTagExistsError)
			err := sendCancelWorkflowRun(workerCtx, pluginCtx, logger, queuedJob, metricsData)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
			}
			skipPrep = true
		}
	}

	if pluginCtx.Err() != nil {
		// logger.WarnContext(pluginCtx, "context canceled during vm template check")
		pluginGlobals.RetryChannel <- true
		return pluginCtx, fmt.Errorf("context canceled during vm template check")
	}

	if !skipPrep {

		// Get runner registration token
		var runnerRegistration *github.RegistrationToken
		var response *github.Response
		var err error
		if isRepoSet {
			pluginCtx, runnerRegistration, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.RegistrationToken, *github.Response, error) {
				runnerRegistration, resp, err := githubClient.Actions.CreateRegistrationToken(context.Background(), pluginConfig.Owner, pluginConfig.Repo)
				return runnerRegistration, resp, err
			})
		} else {
			pluginCtx, runnerRegistration, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.RegistrationToken, *github.Response, error) {
				runnerRegistration, resp, err := githubClient.Actions.CreateOrganizationRegistrationToken(context.Background(), pluginConfig.Owner)
				return runnerRegistration, resp, err
			})
		}
		if err != nil {
			logger.ErrorContext(pluginCtx, "error creating registration token", "err", err, "response", response)
			metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil
		}
		if *runnerRegistration.Token == "" {
			logger.ErrorContext(pluginCtx, "registration token is empty; something wrong with github or your service token", "response", response)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil
		}

		if pluginCtx.Err() != nil {
			// logger.WarnContext(pluginCtx, "context canceled before ObtainAnkaVM")
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("context canceled before ObtainAnkaVM")
		}

		// Obtain Anka VM (and name)
		vm, err := ankaCLI.ObtainAnkaVM(workerCtx, pluginCtx, ankaTemplate)
		if vm != nil {
			queuedJob.AnkaVM.Name = vm.Name
		}
		var wrappedVmJSON []byte
		var wrappedVmErr error
		if vm != nil {
			wrappedVM := internalGithub.QueueJob{
				Type:        "anka.VM",
				AnkaVM:      queuedJob.AnkaVM,
				WorkflowJob: queuedJob.WorkflowJob,
			}
			wrappedVmJSON, wrappedVmErr = json.Marshal(wrappedVM)
			if wrappedVmErr != nil {
				// logger.ErrorContext(pluginCtx, "error marshalling vm to json", "err", wrappedVmErr)
				ankaCLI.AnkaDelete(workerCtx, pluginCtx, vm.Name)
				pluginGlobals.RetryChannel <- true
				return pluginCtx, fmt.Errorf("error marshalling vm to json: %s", wrappedVmErr.Error())
			}
		}
		if pluginCtx.Err() != nil {
			ankaCLI.AnkaDelete(workerCtx, pluginCtx, vm.Name)
			return pluginCtx, fmt.Errorf("context canceled after ObtainAnkaVM")
		}

		// free up other plugins to run
		if workerGlobals.IsAPluginPreparingState() == pluginConfig.Name {
			workerGlobals.UnsetAPluginIsPreparing()
		}

		pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

		dbErr := databaseContainer.Client.RPush(pluginCtx, pluginQueueName, wrappedVmJSON).Err()
		if dbErr != nil {
			// logger.ErrorContext(pluginCtx, "error pushing vm data to database", "err", dbErr)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("error pushing vm data to database: %s", dbErr.Error())
		}
		if err != nil {
			// this is thrown, for example, when there is no capacity on the host
			// we must be sure to create the DB entry so cleanup happens properly
			logger.ErrorContext(pluginCtx, "error obtaining anka vm", "err", err)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil
		}

		if pluginCtx.Err() != nil {
			// logger.WarnContext(pluginCtx, "context canceled after ObtainAnkaVM")
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("context canceled after ObtainAnkaVM")
		}

		// Install runner
		installRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "install-runner.bash")
		registerRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "register-runner.bash")
		startRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "start-runner.bash")
		_, installRunnerErr := os.Stat(installRunnerPath)
		_, registerRunnerErr := os.Stat(registerRunnerPath)
		_, startRunnerErr := os.Stat(startRunnerPath)
		if installRunnerErr != nil || registerRunnerErr != nil || startRunnerErr != nil {
			// logger.ErrorContext(pluginCtx, "must include install-runner.bash, register-runner.bash, and start-runner.bash in "+globals.PluginsPath+"/handlers/github/", "err", err)
			err := sendCancelWorkflowRun(workerCtx, pluginCtx, logger, queuedJob, metricsData)
			if err != nil {
				logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
			}
			return pluginCtx, fmt.Errorf("must include install-runner.bash, register-runner.bash, and start-runner.bash in " + workerGlobals.PluginsPath + "/handlers/github/")
		}

		// Copy runner scripts to VM
		logger.DebugContext(pluginCtx, "copying install-runner.bash, register-runner.bash, and start-runner.bash to vm")
		err = ankaCLI.AnkaCopyIntoVM(pluginCtx,
			queuedJob.AnkaVM.Name,
			installRunnerPath,
			registerRunnerPath,
			startRunnerPath,
		)
		if err != nil {
			// logger.ErrorContext(pluginCtx, "error executing anka copy", "err", err)
			metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("error executing anka copy: %s", err.Error())
		}
		select {
		case <-pluginGlobals.JobChannel:
			logger.WarnContext(pluginCtx, "completed job found before installing runner")
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			return pluginCtx, nil
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before install runner")
			return pluginCtx, nil
		default:
		}

		// Install runner
		logger.DebugContext(pluginCtx, "installing github runner inside of vm")
		installRunnerErr = ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./install-runner.bash")
		if installRunnerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing install-runner.bash", "err", installRunnerErr)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, nil // do not return error here; curl can fail and we need to retry
		}
		// Register runner
		select {
		case <-pluginGlobals.JobChannel:
			logger.InfoContext(pluginCtx, "completed job found before registering runner")
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			return pluginCtx, nil
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before register runner")
			return pluginCtx, nil
		default:
		}
		logger.DebugContext(pluginCtx, "registering github runner inside of vm", "queuedJob", queuedJob)
		registerRunnerErr = ankaCLI.AnkaRun(pluginCtx,
			queuedJob.AnkaVM.Name,
			"./register-runner.bash",
			queuedJob.AnkaVM.Name,
			*runnerRegistration.Token,
			repositoryURL,
			strings.Join(queuedJob.WorkflowJob.Labels, ","),
			pluginConfig.RunnerGroup,
		)
		if registerRunnerErr != nil {
			// logger.ErrorContext(pluginCtx, "error executing register-runner.bash", "err", registerRunnerErr)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("error executing register-runner.bash: %s", registerRunnerErr.Error())
		}
		defer removeSelfHostedRunner(workerCtx, pluginCtx, *vm, &queuedJob, metricsData)
		// Start runner
		select {
		case <-pluginGlobals.JobChannel:
			logger.InfoContext(pluginCtx, "completed job found before starting runner")
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			return pluginCtx, nil
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before start runner")
			return pluginCtx, nil
		default:
		}
		logger.DebugContext(pluginCtx, "starting github runner inside of vm")
		startRunnerErr = ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./start-runner.bash")
		if startRunnerErr != nil {
			// logger.ErrorContext(pluginCtx, "error executing start-runner.bash", "err", startRunnerErr)
			pluginGlobals.RetryChannel <- true
			return pluginCtx, fmt.Errorf("error executing start-runner.bash: %s", startRunnerErr.Error())
		}

		select {
		case <-pluginGlobals.JobChannel:
			logger.InfoContext(pluginCtx, "completed job found before jobCompleted checks")
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			return pluginCtx, nil
		case <-pluginCtx.Done():
			logger.WarnContext(pluginCtx, "context canceled before jobCompleted checks")
			return pluginCtx, nil
		default:
		}
	} // skipPrep

	logger.InfoContext(pluginCtx, "finished preparing anka VM with actions runner")

	// Watch for job completion
	pluginCtx, err = watchForJobCompletion(
		workerCtx,
		pluginCtx,
		pluginQueueName,
		mainInProgressQueueName,
	)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error watching for job completion", "err", err)
		return pluginCtx, err
	}

	fmt.Println(pluginConfig.Name, "Run END ===================")
	return pluginCtx, nil
}

// removeSelfHostedRunner handles removing a registered runner if the registered runner was orphaned somehow
// it's extra safety should the runner not be registered with --ephemera
func removeSelfHostedRunner(
	workerCtx context.Context,
	pluginCtx context.Context,
	vm internalAnka.VM,
	queuedJob *internalGithub.QueueJob,
	metricsData *metrics.MetricsDataLock,
) {
	var err error
	var runnersList *github.Runners
	var response *github.Response
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		fmt.Printf("{\"time\": \"%s\", \"function\": \"removeSelfHostedRunner\", \"error\": \"%s\"}\n", time.Now().Format(time.RFC3339), err.Error())
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin from context", "err", err)
	}
	githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting github client from context", "err", err)
	}
	isRepoSet, err := config.GetIsRepoSetFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting isRepoSet from context", "err", err)
	}
	// we don't use cancelled here since the registration will auto unregister after the job finishes
	if queuedJob.WorkflowJob.Conclusion != nil && (*queuedJob.WorkflowJob.Conclusion == "failure") {
		logger.InfoContext(pluginCtx, "attempting to remove self-hosted runner", "job_id", *queuedJob.WorkflowJob.ID)
		if isRepoSet {
			pluginCtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.Runners, *github.Response, error) {
				runnersList, resp, err := githubClient.Actions.ListRunners(context.Background(), pluginConfig.Owner, pluginConfig.Repo, &github.ListRunnersOptions{})
				return runnersList, resp, err
			})
		} else {
			pluginCtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.Runners, *github.Response, error) {
				runnersList, resp, err := githubClient.Actions.ListOrganizationRunners(context.Background(), pluginConfig.Owner, &github.ListRunnersOptions{})
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
					err := sendCancelWorkflowRun(workerCtx, pluginCtx, logger, *queuedJob, metricsData)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error sending cancel workflow run", "err", err)
						return
					}
					if isRepoSet {
						pluginCtx, _, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.Response, *github.Response, error) {
							response, err := githubClient.Actions.RemoveRunner(context.Background(), pluginConfig.Owner, pluginConfig.Repo, *runner.ID)
							return response, nil, err
						})
					} else {
						pluginCtx, _, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.Response, *github.Response, error) {
							response, err := githubClient.Actions.RemoveOrganizationRunner(context.Background(), pluginConfig.Owner, *runner.ID)
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
}
