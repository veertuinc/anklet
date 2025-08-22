package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
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

func watchJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
	mainInProgressQueueName string,
) (context.Context, error) {
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
		// Check for shutdown signal more frequently
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Warn(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		}

		select {
		case <-pluginCtx.Done():
			logging.Warn(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		default:
			time.Sleep(time.Duration(jobStatusCheckIntervalSeconds) * time.Second)
			queuedJobJSON, err := databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, 0).Result()
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			queuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](queuedJobJSON)
			if err != nil || typeErr != nil {
				logging.Error(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "queuedJobJSON", queuedJobJSON)
				return pluginCtx, err
			}
			if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
				logging.Info(pluginCtx, "job completed",
					"job_id", queuedJob.WorkflowJob.ID,
					"conclusion", queuedJob.WorkflowJob.Conclusion,
				)
				metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
				if err != nil {
					return pluginCtx, err
				}
				switch *queuedJob.WorkflowJob.Conclusion {
				case "success", "":
					metricsData.IncrementTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx)
					err = metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: pluginConfig.Name,
						},
						LastSuccessfulRun:       time.Now(),
						LastSuccessfulRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
					})
					if err != nil {
						return pluginCtx, fmt.Errorf("error updating plugin metrics: %s", err.Error())
					}
				case "failure":
					metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
					err = metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: pluginConfig.Name,
						},
						LastFailedRun:       time.Now(),
						LastFailedRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
					})
					if err != nil {
						return pluginCtx, fmt.Errorf("error updating plugin metrics: %s", err.Error())
					}
				}
				return pluginCtx, nil
			}

			mainInProgressQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			if mainInProgressQueueJobJSON != "" {
				if logCounter%2 == 0 {
					if firstLog {
						logging.Info(pluginCtx, "job found registered runner and is now in progress", "job_id", *queuedJob.WorkflowJob.ID)
						firstLog = false
					}
				}
			} else {
				if !alreadyLogged {
					logging.Info(pluginCtx, "job is waiting for registered runner", "job_id", *queuedJob.WorkflowJob.ID)
					alreadyLogged = true
				}
			}

			var registrationTimeoutSeconds int
			if pluginConfig.RegistrationTimeoutSeconds <= 0 {
				registrationTimeoutSeconds = 80
			} else {
				registrationTimeoutSeconds = pluginConfig.RegistrationTimeoutSeconds
			}
			if logCounter == int(registrationTimeoutSeconds/jobStatusCheckIntervalSeconds) {
				// after X seconds, check to see if the job has started in the in_progress queue
				// if not, then the runner registration failed
				if mainInProgressQueueJobJSON == "" {
					logging.Error(pluginCtx, "waiting for runner registration timed out, will retry")
					pluginGlobals.RetryChannel <- "waiting_for_runner_registration_timed_out"
					queuedJob.Action = "remove_self_hosted_runner" // required or removeSelfHostedRunner won't run
					err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
					if err != nil {
						logging.Error(pluginCtx, "error updating job in db", "err", err)
					}
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
		// 	logging.Error(pluginCtx, "error executing githubClient.Actions.GetWorkflowJobByID", "err", err, "response", response)
		// 	return
		// }
		// if *currentJob.Status == "completed" {
		// 	jobCompleted = true
		// 	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("conclusion", *currentJob.Conclusion))
		// 	logging.Info(pluginCtx, "job completed", "job_id", workflowRunJob.JobID)
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
		// 	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		// 		logging.Warn(pluginCtx, "context canceled during job status check")
		// 		return
		// 	}
		// 	logging.Info(pluginCtx, "job still in progress", "job_id", workflowRunJob.JobID)
		// 	time.Sleep(5 * time.Second) // Wait before checking the job status again
		// }
	}
}

func extractLabelValue(labels []string, prefix string) string {
	for _, label := range labels {
		if result, found := strings.CutPrefix(label, prefix); found {
			return result
		}
	}
	return ""
}

func sendCancelWorkflowRun(
	workerCtx context.Context,
	pluginCtx context.Context,
	queuedJob internalGithub.QueueJob,
) error {
	githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		return err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return err
	}
	cancelSent := false
	for {
		newPluginCtx, workflowRun, _, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.WorkflowRun, *github.Response, error) {
			workflowRun, resp, err := githubClient.Actions.GetWorkflowRunByID(context.Background(), pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.RunID)
			return workflowRun, resp, err
		})
		if err != nil {
			logging.Error(newPluginCtx, "error getting workflow run by ID", "err", err)
			return err
		}
		pluginCtx = newPluginCtx
		if *workflowRun.Status == "completed" ||
			(workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") ||
			cancelSent {
			metricsData.IncrementTotalCanceledRunsSinceStart(workerCtx, pluginCtx)
			err = metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
				PluginBase: &metrics.PluginBase{
					Name: pluginConfig.Name,
				},
				LastCanceledRun:       time.Now(),
				LastCanceledRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
			})
			if err != nil {
				return fmt.Errorf("error updating plugin metrics: %s", err.Error())
			}
			break
		} else {
			logging.Warn(pluginCtx, "workflow run is still active... waiting for cancellation so we can clean up...", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
			if !cancelSent { // this has to happen here so that it doesn't error with "409 Cannot cancel a workflow run that is completed. " if the job is already cancelled
				newPluginCtx, cancelResponse, _, cancelErr := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
					resp, err := githubClient.Actions.CancelWorkflowRunByID(context.Background(), pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.RunID)
					return resp, nil, err
				})
				// don't use cancelResponse.Response.StatusCode or else it'll error with SIGSEV
				if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
					logging.Error(newPluginCtx, "error executing githubClient.Actions.CancelWorkflowRunByID", "err", cancelErr, "response", cancelResponse)
					return cancelErr
				}
				pluginCtx = newPluginCtx
				cancelSent = true
				logging.Warn(pluginCtx, "sent cancel workflow run", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
			}
			// Check for shutdown signal before sleeping
			if workerCtx.Err() != nil || pluginCtx.Err() != nil {
				logging.Warn(pluginCtx, "context canceled while waiting for workflow cancellation")
				return fmt.Errorf("context canceled while waiting for workflow cancellation")
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
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int("check_id", randomInt))
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting globals from context", "err", err)
		os.Exit(1)
	}
	pluginGlobals, err := internalGithub.GetPluginGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin global from context", "err", err)
		os.Exit(1)
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin from context", "err", err)
		os.Exit(1)
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting database from context", "err", err)
		os.Exit(1)
	}
	checkForCompletedJobsContext := context.Background() // needed so we can finalize database changes without orphaning or losing jobs

	for {
		var updateDB = false
		// BE VERY CAREFUL when you use return here. You could orphan the job if you're not careful.
		pluginGlobals.IncrementCheckForCompletedJobsRunCount()
		// do not use 'continue' in the loop or else the ranOnce won't happen
		// logging.Dev(pluginCtx, "checkForCompletedJobs "+pluginConfig.Name+" | getFirstCheckForCompletedJobsRan "+fmt.Sprint(pluginGlobals.GetFirstCheckForCompletedJobsRan()))

		// do NOT use context cancellation here or it will orphan the job

		// must come before the select below
		if workerGlobals.ReturnAllToMainQueue.Load() {
			logging.Warn(pluginCtx, "main worker is attempting to return all jobs to main queue")
			if !pluginGlobals.IsUnreturnable() {
				select {
				case <-pluginGlobals.ReturnToMainQueue:
				default:
					pluginGlobals.ReturnToMainQueue <- "return_all_to_main_queue"
				}
			}
			select { // we don't care about other jobs in the channel
			case <-pluginGlobals.JobChannel:
			default:
			}
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		}

		select {
		case <-pluginGlobals.PausedCancellationJobChannel:
			// logging.Debug(pluginCtx, " checkForCompletedJobs -> pausedCancellationJobChannel")
			pluginGlobals.PausedCancellationJobChannel <- internalGithub.QueueJob{Action: "finish"} // send second one so cleanup doesn't run
			return
		case retryReason := <-pluginGlobals.RetryChannel:
			select {
			case <-pluginGlobals.ReturnToMainQueue:
			default:
				logging.Info(pluginCtx, "retry channel job", "retryReason", retryReason)
				if retryReason == "context_canceled" {
					return
				}
				pluginGlobals.ReturnToMainQueue <- retryReason
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			}
		case job := <-pluginGlobals.JobChannel:
			// logging.Debug(pluginCtx, "checkForCompletedJobs -> job channel job", "job", job)
			if job.Action == "finish" {
				return
			}
			if job.Action == "cancel" {
				// logging.Debug(pluginCtx, "checkForCompletedJobs -> cancel job", "job", job)
				err := sendCancelWorkflowRun(workerCtx, pluginCtx, job)
				if err != nil {
					logging.Error(pluginCtx, "error sending cancel workflow run", "err", err)
				}
				job.Action = "canceled"
				err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &job)
				if err != nil {
					logging.Error(pluginCtx, "error updating job in db", "err", err)
				}
			}
		// case <-pluginCtx.Done():
		// 	// logging.Dev(pluginCtx, "checkForCompletedJobs "+pluginConfig.Name+" pluginCtx.Done()")
		// 	return
		default:
		}

		// get the job ID
		existingJobString, err := databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, 0).Result()
		if err == redis.Nil || existingJobString == "" {
			logging.Debug(pluginCtx, "checkForCompletedJobs -> no job found in pluginQueue")
		} else {
			// logging.Debug(pluginCtx, "checkForCompletedJobs -> job found in pluginQueue")
			queuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](existingJobString)
			if err != nil || typeErr != nil {
				logging.Error(pluginCtx,
					"error unmarshalling job",
					"err", err,
					"typeErr", typeErr,
					"queuedJob", queuedJob,
				)
				return
			}

			// check if in_progress queue, so we can skip cleanup later on
			mainInProgressQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return
			}
			if mainInProgressQueueJobJSON != "" {
				if pluginGlobals.GetCheckForCompletedJobsRunCount()%5 == 0 {
					logging.Info(
						pluginCtx,
						"job is still in progress",
						"job_id", *queuedJob.WorkflowJob.ID,
						"run_count", pluginGlobals.GetCheckForCompletedJobsRunCount(),
						"html_url", *queuedJob.WorkflowJob.HTMLURL,
						"workflow_name", *queuedJob.WorkflowJob.Name,
					)
				}
				// fmt.Println(pluginConfig.Name, " checkForCompletedJobs -> job is in mainInProgressQueue", randomInt)
				mainInProgressQueueJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](mainInProgressQueueJobJSON)
				if err != nil || typeErr != nil {
					logging.Error(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "mainInProgressQueueJobJSON", mainInProgressQueueJobJSON)
					return
				}
				if *queuedJob.WorkflowJob.Status != *mainInProgressQueueJob.WorkflowJob.Status { // prevent running the dbUpdate more than once if not needed
					updateDB = true
				}
				queuedJob.Action = "in_progress"
				queuedJob.WorkflowJob.Status = github.String("in_progress")
			}

			var mainCompletedQueueJobJSON string
			var pluginCompletedQueueJobJSON string
			// check if there is already a completed job queued in the pluginCompletedQueue
			pluginCompletedQueueJobJSON, err = internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, pluginCompletedQueueName)
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return
			}
			if pluginCompletedQueueJobJSON == "" {
				// logging.Debug(pluginCtx, " checkForCompletedJobs -> job is not in pluginCompletedQueue")
				// check if there is already a completed job queued in the mainCompletedQueue
				mainCompletedQueueJobJSON, err = internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, mainCompletedQueueName)
				if err != nil {
					logging.Error(pluginCtx, "error searching in queue", "error", err)
					return
				}
			}

			if pluginCompletedQueueJobJSON != "" {
				logging.Debug(pluginCtx, "checkForCompletedJobs -> job is in pluginCompletedQueue")
				queuedJob.WorkflowJob.Status = github.String("completed")
				queuedJob.Action = "finish"
				select {
				case pluginGlobals.JobChannel <- queuedJob:
				default:
					// // remove the completed job we found
					// _, err = databaseContainer.Client.Del(pluginCtx, pluginCompletedQueueName).Result()
					// if err != nil {
					// 	logging.Error(pluginCtx, "error removing completedJob from "+pluginCompletedQueueName, "err", err)
					// 	return
					// }
					// fmt.Println("checkForCompletedJobs -> deleted existing job from "+pluginCompletedQueueName, "queuedJob", queuedJob)
				}
				updateDB = true
			}

			if mainCompletedQueueJobJSON != "" {
				logging.Debug(pluginCtx, "checkForCompletedJobs -> job is in mainCompletedQueue")
				// remove the completed job we found
				success, err := databaseContainer.Client.LRem(checkForCompletedJobsContext, mainCompletedQueueName, 1, mainCompletedQueueJobJSON).Result()
				if err != nil {
					logging.Error(pluginCtx,
						"error removing completedJob from "+mainCompletedQueueName,
						"err", err,
						"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
					)
					return
				}
				if success != 1 {
					logging.Error(
						pluginCtx,
						"non-successful removal of completedJob from "+mainCompletedQueueName,
						"err", err,
						"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
					)
					return
				}

				completedQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](mainCompletedQueueJobJSON)
				if err != nil || typeErr != nil {
					logging.Error(pluginCtx,
						"error unmarshalling job",
						"err", err,
						"typeErr", typeErr,
						"queuedJob", queuedJob,
					)
					return
				}
				queuedJob.WorkflowJob.Status = completedQueuedJob.WorkflowJob.Status
				queuedJob.WorkflowJob.Conclusion = completedQueuedJob.WorkflowJob.Conclusion
				// add a task for the completed job so we know the clean up
				logging.Debug(pluginCtx, "checkForCompletedJobs -> adding completed job to pluginCompletedQueue")
				_, err = databaseContainer.Client.LPush(checkForCompletedJobsContext, pluginCompletedQueueName, mainCompletedQueueJobJSON).Result()
				if err != nil {
					logging.Error(pluginCtx, "error inserting completed job into list", "err", err)
					return
				}
				select { // handle when pluginGlobals.RetryChannel sends something to pluginGlobals.JobChannel already so we don't orphan anything
				case <-pluginGlobals.JobChannel:
				default:
				}
				// reset the queue target index every other run so we don't leave any jobs behind at the lower indexes but still keep moving ahead
				if workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].PluginRunCount.Load()%2 == 0 {
					workerGlobals.ResetQueueTargetIndex() // make sure we reset the index so we don't leave any jobs behind at the lower indexes
				}

				pluginGlobals.JobChannel <- queuedJob
				updateDB = true
			}

			// if in progress, but no completed job found, we need to check the status of the job in github's API
			if !pluginGlobals.GetFirstCheckForCompletedJobsRan() &&
				mainCompletedQueueJobJSON == "" && pluginCompletedQueueJobJSON == "" &&
				mainInProgressQueueJobJSON != "" {
				logging.Debug(pluginCtx, "checkForCompletedJobs -> job is in mainInProgressQueue, checking status from API")
				queuedJob, err = internalGithub.UpdateJobsWorkflowJobStatus(workerCtx, pluginCtx, &queuedJob)
				if err != nil {
					logging.Error(pluginCtx, "error checking workflow job status", "err", err)
					return
				}
				if queuedJob.WorkflowJob.Status != nil &&
					(*queuedJob.WorkflowJob.Status == "completed" ||
						*queuedJob.WorkflowJob.Status == "failed" ||
						*queuedJob.WorkflowJob.Status == "cancelled") {
					// add a task for the completed job so we clean it up
					queuedJobJSON, err := json.Marshal(queuedJob)
					if err != nil {
						logging.Error(pluginCtx, "error marshalling queued job", "err", err)
						return
					}
					_, err = databaseContainer.Client.LPush(pluginCtx, pluginCompletedQueueName, queuedJobJSON).Result()
					if err != nil {
						logging.Error(pluginCtx, "error inserting completed job into list", "err", err)
						return
					}
				}
				if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "in_progress" {
					queuedJob.Action = "in_progress"
				}
				pluginGlobals.JobChannel <- queuedJob
				updateDB = true
			}

			// update the job in the database so we can get the new status for subsequent steps
			if updateDB {
				// logging.Debug(pluginCtx, "checkForCompletedJobs -> updating job in database")
				err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
				if err != nil {
					logging.Error(pluginCtx, "error updating job in db", "err", err)
				}
			}
		}

		// logging.Debug(pluginCtx, "checkForCompletedJobs end loop")

		// Check for shutdown signal before sleeping
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Warn(pluginCtx, "context canceled in checkForCompletedJobs loop")
			return
		}

		pluginGlobals.SetFirstCheckForCompletedJobsRan(true)

		time.Sleep(time.Second * 3)
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
	mainCompletedQueueName string,
	pausedQueueName string,
	onStartRun bool,
) {

	// create an idependent copy of the pluginCtx so we can do cleanup even if pluginCtx got "context canceled"
	cleanupContext := context.Background()

	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting logger from context", "err", err)
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("logger"), logger)

	logging.Info(pluginCtx, "cleanup | started", "onStartRun", onStartRun)

	pluginGlobals, err := internalGithub.GetPluginGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin global from context", "err", err)
		return
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin from context", "err", err)
		return
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting worker global from context", "err", err)
		return
	}

	pluginGlobals.CleanupMutex.Lock()

	defer func() {
		if pluginGlobals.CleanupMutex != nil {
			// fmt.Println(pluginConfig.Name, "cleanup | unlocking plugin cleanup mutex")
			pluginGlobals.CleanupMutex.Unlock()
		}
		if !onStartRun {
			// fmt.Println(pluginConfig.Name, "cleanup | unlocking plugin preparing state")
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
		}
		if !workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Load() {
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Store(true)
		} else {
			// don't unpause if it's the initial start and first plugin
			if workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Load() {
				workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
			}
		}
	}()

	serviceDatabase, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting database from context", "err", err)
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("database"), serviceDatabase)
	cleanupContext, cancel := context.WithCancel(cleanupContext)
	defer func() {
		cancel()
	}()
	databaseContainer, err := database.GetDatabaseFromContext(cleanupContext)
	if err != nil {
		logging.Error(pluginCtx, "error getting database from context", "err", err)
		return
	}

	// get the original job with the latest status and check if it's running
	originalJobJSON, err := databaseContainer.Client.LIndex(cleanupContext, pluginQueueName, 0).Result()
	if err != nil && err != redis.Nil {
		logging.Error(pluginCtx, "error getting job from the list", "err", err)
		return
	}
	if err == redis.Nil {
		return // nothing to clean up
	}
	originalQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](originalJobJSON)
	if err != nil || typeErr != nil {
		logging.Error(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "originalJobJSON", originalJobJSON)
		return
	}

	select {
	case <-pluginGlobals.PausedCancellationJobChannel: // no cleanup necessary, it was picked up by another host
		logging.Debug(pluginCtx, "cleanup | pausedCancellationJobChannel")
		// remove from paused queue so other hosts won't try to pick it up anymore.
		err = internalGithub.DeleteFromQueue(pluginCtx, *originalQueuedJob.WorkflowJob.ID, pausedQueueName)
		if err != nil {
			logging.Error(pluginCtx, "error deleting from in_progress queue", "err", err)
		}
		return
	default:
	}

	// if the job is running, we don't need to clean it up yet
	if originalQueuedJob.WorkflowJob.Status != nil &&
		(*originalQueuedJob.WorkflowJob.Status == "in_progress" ||
			(workerGlobals.ReturnAllToMainQueue.Load() && originalQueuedJob.Action == "in_progress")) {
		logging.Debug(pluginCtx, "cleanup | job is still running; skipping cleanup")
		return
	}

	for {
		// do NOT use context cancellation here (or look for ReturnAllToMainQueue) or it will orphan the job

		var queuedJob internalGithub.QueueJob
		var typeErr error
		var cleaningJobJSON string

		// get the cleaning job from the cleaning list
		cleaningJobJSON, err = databaseContainer.Client.LIndex(cleanupContext, pluginQueueName+"/cleaning", 0).Result()
		if err != nil && err != redis.Nil {
			logging.Error(pluginCtx, "error getting job from the list", "err", err)
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
				logging.Debug(pluginCtx, "cleanup | no job to clean up from "+pluginQueueName)
				return // nothing to clean up
			} else if err != nil {
				logging.Error(pluginCtx, "error popping job from the list", "err", err)
				return
			}
		}

		queuedJob, err, typeErr = database.Unwrap[internalGithub.QueueJob](cleaningJobJSON)
		if err != nil || typeErr != nil {
			if err == redis.Nil {
				logging.Debug(pluginCtx, "no job to clean up from "+pluginQueueName)
				return // nothing to clean up
			}
			logging.Error(pluginCtx,
				"error unmarshalling job",
				"err", err,
				"typeErr", typeErr,
				"cleaningJobJSON", cleaningJobJSON,
			)
			return
		}

		switch queuedJob.Type {
		case "anka.VM":
			logging.Debug(pluginCtx, "cleanup | anka.VM | queuedJob", "queuedJob", queuedJob)
			ankaCLI, err := internalAnka.GetAnkaCLIFromContext(pluginCtx)
			if err != nil {
				logging.Error(pluginCtx, "error getting ankaCLI from context", "err", err)
				return
			}
			if strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEBUG" || strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEV" {
				err = ankaCLI.AnkaCopyOutOfVM(pluginCtx, queuedJob.AnkaVM.Name, "/Users/anka/actions-runner/_diag", "/tmp/"+queuedJob.AnkaVM.Name)
				if err != nil {
					logging.Warn(pluginCtx, "error copying actions runner out of vm", "err", err)
				}
			}
			err = ankaCLI.AnkaDelete(workerCtx, pluginCtx, queuedJob.AnkaVM.Name)
			if err != nil {
				logging.Error(pluginCtx, "error deleting vm", "err", err)
			}
			databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			continue // required to keep processing tasks in the db list
		case "WorkflowJobPayload": // MUST COME LAST
			logging.Debug(pluginCtx, "cleanup | WorkflowJobPayload | queuedJob", "queuedJob", queuedJob)
			// delete the in_progress queue's index that matches the wrkflowJobID
			// use cleanupContext so we don't orphan the job in the DB on context cancel of pluginCtx
			err = internalGithub.DeleteFromQueue(cleanupContext, *queuedJob.WorkflowJob.ID, mainInProgressQueueName)
			if err != nil {
				logging.Error(pluginCtx, "error deleting from in_progress queue", "err", err)
			}

			// clean up completed jobs from the database
			if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
				logging.Debug(pluginCtx, "cleanup | removing completed job from database", "queuedJob", queuedJob)
				// cleanup the mainCompletedQueue job if it exists so we don't orphan it
				// needed because we don't move it to the pluginCompletedQueue under specific conditions
				mainCompletedQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, mainCompletedQueueName)
				if err != nil {
					logging.Error(pluginCtx, "error searching in queue", "error", err)
				}
				if mainCompletedQueueJobJSON != "" {
					success, err := databaseContainer.Client.LRem(cleanupContext, mainCompletedQueueName, 1, mainCompletedQueueJobJSON).Result()
					if err != nil {
						logging.Error(pluginCtx,
							"error removing completedJob from "+mainCompletedQueueName,
							"err", err,
							"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
						)
						return
					}
					if success != 1 {
						logging.Error(
							pluginCtx,
							"non-successful removal of completedJob from "+mainCompletedQueueName,
							"err", err,
							"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
						)
						return
					}
				}
				databaseContainer.Client.Del(cleanupContext, pluginCompletedQueueName)
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
				break
			}
			// return it to the queue if the job isn't completed yet
			// if we don't, we could suffer from a situation where a completed job comes in and is orphaned
			select {
			case <-pluginGlobals.PausedCancellationJobChannel:
				// logging.Debug(pluginCtx, "cleanup | WorkflowJobPayload | PausedCancellationJobChannel")
				// if the job was paused, we need to remove it
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
				return
			case reason := <-pluginGlobals.ReturnToMainQueue:
				logging.Warn(pluginCtx, "pushing job from "+pluginQueueName+"/cleaning back to "+mainQueueName, "reason", reason)
				// do not use RPopLPush due to hash tag issue
				// remove from cleaning queue so other hosts won't try to pick it up anymore.
				cleaningJobJSON, err = databaseContainer.Client.RPop(cleanupContext, pluginQueueName+"/cleaning").Result()
				if err != nil {
					logging.Error(pluginCtx, "error removing job from cleaning queue", "err", err)
					return
				}
				// set proper status back to queued
				cleaningQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](cleaningJobJSON)
				if err != nil || typeErr != nil {
					logging.Error(pluginCtx, "error unmarshalling job", "err", err, "typeErr", typeErr, "cleaningJobJSON", cleaningJobJSON)
					return
				}
				cleaningQueuedJob.Action = "queued"
				// don't increment attempts if the job is not enough resources for this host
				// else we'll accidentally send a cancellation for a job that may run elsewhere
				if reason != "not_enough_host_resources" {
					cleaningQueuedJob.Attempts++
				}
				cleaningJobJSON, err := json.Marshal(cleaningQueuedJob)
				if err != nil {
					logging.Error(pluginCtx, "error marshalling job", "err", err)
					return
				}
				// push to the target queue
				_, err = databaseContainer.Client.LPush(cleanupContext, mainQueueName, cleaningJobJSON).Result()
				if err != nil {
					logging.Error(pluginCtx, "error pushing job back to queued", "err", err)
					return
				}
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			default:
				if queuedJob.WorkflowJob.Status != nil &&
					(*queuedJob.WorkflowJob.Status == "completed" || *queuedJob.WorkflowJob.Status == "failed") { // don't send it back to the queue if the job is completed
					databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
					return
				}

				logging.Warn(pluginCtx, "pushing job back to "+pluginQueueName, "queuedJob", queuedJob)
				_, err := databaseContainer.Client.RPopLPush(cleanupContext, pluginQueueName+"/cleaning", pluginQueueName).Result()
				if err != nil {
					logging.Error(pluginCtx, "error pushing job back to queued", "err", err)
					return
				}
				databaseContainer.Client.Del(cleanupContext, pluginQueueName+"/cleaning")
			}
		default:
			logging.Error(pluginCtx, "unknown job type", "job", queuedJob)
			return
		}
		return // don't delete the queued/servicename
	}
}

func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
) (context.Context, error) {
	logging.Info(pluginCtx, "running github plugin")

	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}

	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int("hostCPUCount", workerGlobals.HostCPUCount))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Uint64("hostMemoryBytes", workerGlobals.HostMemoryBytes))

	// must come after first cleanup
	pluginGlobals := internalGithub.PluginGlobals{
		FirstCheckForCompletedJobsRan: 0,                    // atomic: 0 = false
		RetryChannel:                  make(chan string, 1), // TODO: do we need this if we have ReturnToMainQueue?
		CleanupMutex:                  &sync.Mutex{},
		JobChannel:                    make(chan internalGithub.QueueJob, 1),
		ReturnToMainQueue:             make(chan string, 1),
		PausedCancellationJobChannel:  make(chan internalGithub.QueueJob, 1),
		CheckForCompletedJobsRunCount: 0, // atomic counter
		Unreturnable:                  false,
	}
	// TODO: replace this with workerGlobals
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("pluginglobals"), &pluginGlobals)

	configFileName, err := config.GetConfigFileNameFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	if pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in %s:plugins:%s<token/private_key>", configFileName, pluginConfig.Name)
	}
	if pluginConfig.PrivateKey != "" && (pluginConfig.AppID == 0 || pluginConfig.InstallationID == 0) {
		return pluginCtx, fmt.Errorf("private_key, app_id, and installation_id must all be set in %s:plugins:%s<token/private_key>", configFileName, pluginConfig.Name)
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
	// if pluginConfig.Repo == "" {
	// 	logging.Panic(workerCtx, pluginCtx, "repo is not set in anklet.yaml:plugins:"+pluginConfig.Name+":repo")
	// }

	var githubClient *github.Client
	githubClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		pluginConfig.PrivateKey,
		pluginConfig.AppID,
		pluginConfig.InstallationID,
		pluginConfig.Token,
	)
	if err != nil {
		// logging.Error(pluginCtx, "error authenticating github client", "err", err)
		return pluginCtx, fmt.Errorf("error authenticating github client: %s", err.Error())
	}
	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	var repositoryURL string
	if pluginConfig.Repo != "" {
		repositoryURL = fmt.Sprintf("https://github.com/%s/%s", pluginConfig.Owner, pluginConfig.Repo)
	} else {
		repositoryURL = fmt.Sprintf("https://github.com/%s", pluginConfig.Owner)
	}
	// pluginCtx = logging.AppendCtx(pluginCtx, slog.String("owner", pluginConfig.Owner))

	// wait group so we can wait for the goroutine to finish before exiting the service
	var wg sync.WaitGroup
	wg.Add(1)

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		// logging.Error(pluginCtx, "error getting database from context", "err", err)
		return pluginCtx, fmt.Errorf("error getting database from context: %s", err.Error())
	}

	mainQueueName := "anklet/jobs/github/queued/" + pluginConfig.Owner
	mainCompletedQueueName := "anklet/jobs/github/completed/" + pluginConfig.Owner
	// hash tag needed for avoiding "CROSSSLOT Keys in request don't hash to the same slot"
	pluginQueueName := "anklet/jobs/github/queued/" + pluginConfig.Owner + "/" + "{" + pluginConfig.Name + "}"
	pluginCompletedQueueName := "anklet/jobs/github/completed/" + pluginConfig.Owner + "/" + pluginConfig.Name
	mainInProgressQueueName := "anklet/jobs/github/in_progress/" + pluginConfig.Owner
	pausedQueueName := "anklet/jobs/github/paused/" + pluginConfig.Owner

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		logging.Warn(pluginCtx, "context canceled before checking for jobs")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	}

	defer func() {
		wg.Wait()
		cleanup(
			workerCtx,
			pluginCtx,
			mainQueueName,
			pluginQueueName,
			mainInProgressQueueName,
			pluginCompletedQueueName,
			mainCompletedQueueName,
			pausedQueueName,
			false,
		)
		close(pluginGlobals.JobChannel)
	}()

	// check constantly for a cancelled/completed webhook to be received for our job
	go func() {
		defer func() {
			wg.Done()
		}()
		checkForCompletedJobs(
			workerCtx,
			pluginCtx,
			pluginQueueName,
			pluginCompletedQueueName,
			mainCompletedQueueName,
			mainInProgressQueueName,
		)
	}()
	time.Sleep(1 * time.Second)
	for !pluginGlobals.GetFirstCheckForCompletedJobsRan() {
		logging.Debug(pluginCtx, "waiting for first run checks to complete")
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			return pluginCtx, fmt.Errorf("context canceled while waiting for first checkForCompletedJobs to run")
		}
		time.Sleep(1 * time.Second)
	}
	select {
	case jobFromJobChannel := <-pluginGlobals.JobChannel:
		// return if completed so the cleanup doesn't run twice
		if *jobFromJobChannel.WorkflowJob.Status == "completed" || *jobFromJobChannel.WorkflowJob.Status == "failed" {
			logging.Info(pluginCtx, *jobFromJobChannel.WorkflowJob.Status+" job found at start", "jobFromJobChannel", jobFromJobChannel)
			pluginGlobals.JobChannel <- jobFromJobChannel
			return pluginCtx, nil
		}
		if *jobFromJobChannel.WorkflowJob.Status == "in_progress" { // TODO: make this the same as the loop later on
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			logging.Info(pluginCtx, "watching for job completion", "jobFromJobChannel", jobFromJobChannel)
			pluginCtx, err = watchJobStatus(
				workerCtx,
				pluginCtx,
				pluginQueueName,
				mainInProgressQueueName,
			)
			if err != nil {
				logging.Error(pluginCtx, "error watching for job completion", "err", err)
				return pluginCtx, err
			}
			return pluginCtx, nil
		}
	default:
		// logging.Info(pluginCtx, "no existing in_progress jobs found on initial startup")
		if !workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Load() { // exit early if it's the first time running
			pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
			return pluginCtx, nil
		}
	}

	// finalize cleanup if the service crashed mid-cleanup
	cleanup(
		workerCtx,
		pluginCtx,
		mainQueueName,
		pluginQueueName,
		mainInProgressQueueName,
		pluginCompletedQueueName,
		mainCompletedQueueName,
		pausedQueueName,
		true,
	)
	select {
	case runningJob := <-pluginGlobals.JobChannel:
		// fmt.Println("after first cleanup status", runningJob.WorkflowJob.Status)
		if *runningJob.WorkflowJob.Status == "completed" || *runningJob.WorkflowJob.Status == "failed" {
			logging.Info(pluginCtx, *runningJob.WorkflowJob.Status+" job found at start")
			pluginGlobals.JobChannel <- runningJob
			return pluginCtx, nil
		}
	case <-pluginCtx.Done():
		// logging.Warn(pluginCtx, "context canceled before completed job found")
		return pluginCtx, nil
	default:
	}

	hostHasVmCapacity := internalAnka.HostHasVmCapacity(pluginCtx)
	if !hostHasVmCapacity {
		logging.Warn(pluginCtx, "host does not have vm capacity")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	}

	// // check if another plugin is already running preparation phase
	// for !workerGlobals.IsMyTurnForPrepLock(pluginConfig.Name) {
	// 	logging.Debug(pluginCtx, "not this plugin's turn for prep, waiting...")
	// 	time.Sleep(2 * time.Second)
	// 	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
	// 		return pluginCtx, fmt.Errorf("context canceled while waiting for prep lock")
	// 	}
	// }

	logging.Info(pluginCtx, "checking for jobs....")

	// alreadyNexted := false
	// pluginGlobals.AlreadyNextedPrepLock = &alreadyNexted // important so the next cleanup doesn't advance the prep lock/index

	var queuedJobString string
	var queuedJob internalGithub.QueueJob

	// allow picking up where we left off
	// always get -1 so we get the eldest job in the queue
	queuedJobString, err = databaseContainer.Client.LIndex(pluginCtx, pluginQueueName, -1).Result()
	if err != nil && err != redis.Nil {
		// logging.Error(pluginCtx, "error getting last object from anklet/jobs/github/queued/"+pluginConfig.Owner+"/"+pluginConfig.Name, "err", err)
		return pluginCtx, fmt.Errorf("error getting last object from %s", pluginQueueName)
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
		var pausedQueuedJobString string
		var pausedQueueTargetIndex int64 = 0
		for {
			// Check for shutdown signal more frequently
			if workerCtx.Err() != nil || pluginCtx.Err() != nil {
				logging.Warn(pluginCtx, "context canceled before first check for paused jobs")
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
				return pluginCtx, nil
			}

			// fmt.Println(pluginConfig.Name, "checking for paused jobs")
			// Check the length of the paused queue
			pausedQueueLength, err := databaseContainer.Client.LLen(pluginCtx, pausedQueueName).Result()
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting paused queue length: %s", err.Error())
			}
			if pausedQueueLength == 0 {
				// fmt.Println(pluginConfig.Name, "no paused jobs found (length 0)")
				break
			}
			pausedQueuedJobString, err = internalGithub.PopJobOffQueue(pluginCtx, pausedQueueName, pausedQueueTargetIndex)
			defer func() {
				// there is a chance a failure could cause the paused job to be permanently removed, so make sure it gets put back no matter what
				// BUT first check if the job status has changed to completed/failed - if so, don't put it back
				// fmt.Println(pluginConfig.Name, "end of paused jobs loop iteration")
				if pausedQueuedJobString != "" {
					// Parse the job to get its ID and check current status in DB
					pausedJob, parseErr, typeErr := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
					if parseErr == nil && typeErr == nil && pausedJob.WorkflowJob.ID != nil {
						// Check the current job status in the original host's queue
						currentJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *pausedJob.WorkflowJob.ID, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/{"+pausedJob.PausedOn+"}")
						if err == nil && currentJobJSON != "" {
							currentJob, currentParseErr, currentTypeErr := database.Unwrap[internalGithub.QueueJob](currentJobJSON)
							if currentParseErr == nil && currentTypeErr == nil && currentJob.WorkflowJob.Status != nil {
								if *currentJob.WorkflowJob.Status == "completed" || *currentJob.WorkflowJob.Status == "failed" {
									logging.Info(pluginCtx, "not putting job back in paused queue because it's "+*currentJob.WorkflowJob.Status, "workflowJobID", *pausedJob.WorkflowJob.ID)
									return // Don't put it back in paused queue
								}
							}
						}
					}

					// Job is still active, put it back in paused queue
					err := databaseContainer.Client.LPush(pluginCtx, pausedQueueName, pausedQueuedJobString).Err()
					if err != nil {
						logging.Error(pluginCtx, "error pushing job to paused queue", "err", err)
					}
					// fmt.Println(pluginConfig.Name, "pushed job back to paused queue", pausedQueuedJobString)
				}
			}()
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting paused jobs: %s", err.Error())
			}
			if pausedQueuedJobString == "" {
				// fmt.Println(pluginConfig.Name, "no paused jobs found (empty string)")
				break
			}
			// Process each paused job and find one we can run
			var typeErr error
			pausedQueuedJob, err, typeErr := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
			if err != nil || typeErr != nil {
				return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
			}
			// check if the job is aleady paused on this same host (matches any plugin names)
			if workerGlobals.Plugins[pluginConfig.Plugin][pausedQueuedJob.PausedOn] != nil {
				logging.Info(pluginCtx, "job is already paused on this host by another plugin, skipping")
				pausedQueueTargetIndex++
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}
			err = internalAnka.VmHasEnoughHostResources(pluginCtx, pausedQueuedJob.AnkaVM)
			if err != nil {
				// fmt.Println(pluginConfig.Name, "paused job does not have enough host resources to run")
				pausedQueueTargetIndex++
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}
			err = internalAnka.VmHasEnoughResources(pluginCtx, pausedQueuedJob.AnkaVM)
			if err != nil {
				// fmt.Println(pluginConfig.Name, "paused job does not have enough resources yet on the host")
				pausedQueueTargetIndex++
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}

			logging.Info(pluginCtx, "paused job found to run", "pausedQueuedJob", pausedQueuedJob)

			// pull the workflow job from the currently paused host's queue and put it in the current queue instead
			originalHostJob, err := internalGithub.GetJobFromQueueByKeyAndValue(
				pluginCtx,
				"anklet/jobs/github/queued/"+pluginConfig.Owner+"/{"+pausedQueuedJob.PausedOn+"}",
				"workflow_job.id",
				strconv.FormatInt(*pausedQueuedJob.WorkflowJob.ID, 10),
			)
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting job from paused host's queue: %s", err.Error())
			}
			if originalHostJob == "" {
				// bad/orphaned paused job, remove it from the paused queue
				databaseContainer.Client.LRem(pluginCtx, pausedQueueName, 1, pausedQueuedJobString)
				logging.Error(pluginCtx, "problem getting job from paused host's queue (empty string); removing item from paused queue")
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
				pausedQueueTargetIndex++
				if pausedQueueLength == 1 { // don't go into a forever loop
					break
				}
				continue
			}
			queuedJob, err, typeErr = database.Unwrap[internalGithub.QueueJob](originalHostJob)
			if err != nil || typeErr != nil {
				return pluginCtx, fmt.Errorf("error unmarshalling job: %s", err.Error())
			}
			// make sure it hasn't started running on the other host
			if queuedJob.Action != "paused" {
				logging.Info(pluginCtx, "job is running on the other host, so we can't run it anymore")
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
				return pluginCtx, nil
			}
			// remove it from the old host queue
			databaseContainer.Client.LRem(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/{"+pausedQueuedJob.PausedOn+"}", 1, originalHostJob)
			queuedJobString = originalHostJob
			pausedQueuedJobString = "" // don't push back to the paused queue
			break
		}

		// don't hold on to paused jobs
		if pausedQueuedJobString != "" {
			// Check if job status has changed to completed/failed before putting back
			pausedJob, parseErr, typeErr := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
			shouldPutBack := true
			if parseErr == nil && typeErr == nil && pausedJob.WorkflowJob.ID != nil {
				// Check the current job status in the original host's queue
				currentJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *pausedJob.WorkflowJob.ID, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/{"+pausedJob.PausedOn+"}")
				if err == nil && currentJobJSON != "" {
					currentJob, currentParseErr, currentTypeErr := database.Unwrap[internalGithub.QueueJob](currentJobJSON)
					if currentParseErr == nil && currentTypeErr == nil && currentJob.WorkflowJob.Status != nil {
						if *currentJob.WorkflowJob.Status == "completed" || *currentJob.WorkflowJob.Status == "failed" {
							logging.Info(pluginCtx, "not putting job back in paused queue because it's "+*currentJob.WorkflowJob.Status, "workflowJobID", *pausedJob.WorkflowJob.ID)
							shouldPutBack = false
						}
					}
				}
			}

			if shouldPutBack {
				err := databaseContainer.Client.LPush(pluginCtx, pausedQueueName, pausedQueuedJobString).Err()
				if err != nil {
					logging.Error(pluginCtx, "error pushing job to paused queue", "err", err)
				}
				logging.Debug(pluginCtx, "pushed job back to paused queue", "pausedQueuedJobString", pausedQueuedJobString)
			}
			pausedQueuedJobString = "" // don't let the defer function push it back
		}

		// If not paused job to get, get a job from the main queue
		if queuedJobString == "" {
			// check if the queue length is less than the index and then reset the index if so
			mainQueueLength, err := databaseContainer.Client.LLen(pluginCtx, mainQueueName).Result()
			if err != nil {
				return pluginCtx, fmt.Errorf("error getting main queue length: %s", err.Error())
			}
			if *workerGlobals.QueueTargetIndex > mainQueueLength {
				workerGlobals.ResetQueueTargetIndex()
			}
			queuedJobString, err = internalGithub.PopJobOffQueue(pluginCtx, mainQueueName, *workerGlobals.QueueTargetIndex)
			if err != nil {
				metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
				return pluginCtx, fmt.Errorf("error getting queued jobs: %s", err.Error())
			}
			if queuedJobString == "" { // no queued jobs
				logging.Debug(pluginCtx, "no queued jobs found")
				// free up other plugins to run
				workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
				pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
				workerGlobals.ResetQueueTargetIndex()
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
		// 		logging.Error(pluginCtx, "error removing job from main queue", "err", err)
		// 		return pluginCtx, fmt.Errorf("error removing job from main queue: %s", err.Error())
		// 	}
		// }
		// if err == redis.Nil {
		// 	logging.Debug(pluginCtx, "no queued jobs found")
		// 	globals.ResetQueueIndex()
		// 	completedJobChannel <- internalGithub.QueueJob{} // send true to the channel to stop the check for completed jobs goroutine
		// 	return pluginCtx, nil
		// }
		// if err != nil {
		// 	logging.Error(pluginCtx, "error getting queued jobs", "err", err)
		// 	metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx, logger)
		// 	globals.ResetQueueIndex()
		// 	return pluginCtx, fmt.Errorf("error getting queued jobs: %s", err.Error())
		// }
		// databaseContainer.Client.RPush(pluginCtx, "anklet/jobs/github/queued/"+pluginConfig.Owner+"/"+pluginConfig.Name, queuedJobString)
	}

	logging.Info(
		pluginCtx,
		"queued job found",
		"queuedJob", queuedJob,
	)

	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobID", *queuedJob.WorkflowJob.ID))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("workflowJobRunID", *queuedJob.WorkflowJob.RunID))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowName", *queuedJob.WorkflowJob.WorkflowName))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("workflowJobURL", *queuedJob.WorkflowJob.HTMLURL))
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Int("attempts", queuedJob.Attempts))

	// now that the job is in the plugin's queue, check if the job is already completed so we don't orphan if there is
	// a job in anklet/jobs/github/queued and also a anklet/jobs/github/completed
	pluginGlobals.SetFirstCheckForCompletedJobsRan(false)
	counter := 0

	// Check for shutdown signal before entering the loop
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		logging.Warn(pluginCtx, "context canceled before first check for completed jobs")
		return pluginCtx, nil
	}

	for !pluginGlobals.GetFirstCheckForCompletedJobsRan() {
		// Check for shutdown signal more frequently
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Warn(pluginCtx, "context canceled before while check for completed jobs")
			return pluginCtx, nil
		}

		select {
		case <-pluginCtx.Done():
			logging.Warn(pluginCtx, "context canceled before while check for completed jobs")
			return pluginCtx, nil
		default:
		}
		time.Sleep(1 * time.Second)
		counter++
		if counter%5 == 0 {
			logging.Debug(pluginCtx, "waiting for first check for completed jobs to run", "seconds_waited", counter)
		}
	}
	select {
	case job := <-pluginGlobals.JobChannel:
		if *job.WorkflowJob.Status == "completed" || *job.WorkflowJob.Status == "failed" {
			logging.Info(pluginCtx, *job.WorkflowJob.Status+" job found by checkForCompletedJobs (at start of Run)")
			pluginGlobals.JobChannel <- job // send true to the channel to stop the check for completed jobs goroutine
			return pluginCtx, nil
		}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before vm startup")
		return pluginCtx, nil
	default:
	}
	// if the job has attempts > 0, we need to check the status from the API to see if the job is still even running
	// Github can mark a job completed, but it's not sending the webhook event for completed and it can sit like this for hours
	// TODO: find a way to test this
	if queuedJob.Attempts > 0 {
		if queuedJob.Attempts > 5 {
			logging.Warn(pluginCtx, "job has attempts > 5, cancelling it")
			queuedJob.Action = "cancel"
			queuedJob.WorkflowJob.Status = github.String("completed")
			err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
			if err != nil {
				logging.Error(pluginCtx, "error updating job in db", "err", err)
			}
			pluginGlobals.JobChannel <- queuedJob
			return pluginCtx, nil
		}
		logging.Warn(pluginCtx, "job has attempts > 0, checking status from API to see if the job is still even running")
		queuedJob, err = internalGithub.UpdateJobsWorkflowJobStatus(workerCtx, pluginCtx, &queuedJob)
		if err != nil {
			logging.Error(pluginCtx, "error updating job workflow job status", "err", err)
			pluginGlobals.RetryChannel <- "error_updating_job_workflow_job_status"
			return pluginCtx, nil
		}
		if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
			queuedJob.Action = "finish"
			err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
			if err != nil {
				logging.Error(pluginCtx, "error updating job in db", "err", err)
			}
			pluginGlobals.JobChannel <- queuedJob
			return pluginCtx, nil
		}
		if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "in_progress" {
			logging.Info(pluginCtx, "job is in progress, so we'll wait for it to finish")
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			pluginCtx, err = watchJobStatus(
				workerCtx,
				pluginCtx,
				pluginQueueName,
				mainInProgressQueueName,
			)
			if err != nil {
				logging.Error(pluginCtx, "error watching for job completion", "err", err)
				return pluginCtx, err
			}
			return pluginCtx, nil
		}
	}

	// get anka template
	ankaTemplate := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template:")
	if ankaTemplate == "" {
		logging.Warn(pluginCtx, "warning: unable to find Anka Template specified in labels, cancelling job")
		// TODO: turn this block into a function, then use it throughout the plugin
		queuedJob.Action = "cancel"
		err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
		if err != nil {
			logging.Error(pluginCtx, "error updating job in db", "err", err)
		}
		// TODO
		pluginGlobals.JobChannel <- queuedJob
		return pluginCtx, nil
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

	logging.Info(pluginCtx, "handling anka workflow run job")
	err = metricsData.SetStatus(pluginCtx, "in_progress")
	if err != nil {
		logging.Error(pluginCtx, "error setting plugin status", "err", err)
	}
	queuedJob.Action = "preparing"
	err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
	if err != nil {
		logging.Error(pluginCtx, "error updating job in db", "err", err)
		pluginGlobals.RetryChannel <- "error_updating_job_in_db"
		return pluginCtx, nil
	}

	if queuedJob.AnkaVM.CPUCount == 0 || queuedJob.AnkaVM.MEMBytes == 0 { // no need to get VM info if we already have it

		// Check if host has enough resources for the template
		isRegistryRunning, err := ankaCLI.AnkaRegistryRunning(pluginCtx)
		if err != nil {
			logging.Error(pluginCtx, "error checking if registry is running", "err", err)
			pluginGlobals.RetryChannel <- "anka_registry_not_running"
			return pluginCtx, nil
		}
		if isRegistryRunning {
			ankaRegistryVMInfo, err := internalAnka.GetAnkaRegistryVmInfo(workerCtx, pluginCtx, ankaTemplate, ankaTemplateTag)
			if err != nil {
				if strings.Contains(err.Error(), "not found") {
					logging.Warn(pluginCtx, err.Error())
					queuedJob.Action = "cancel"
					queuedJob.WorkflowJob.Conclusion = github.String("failure")
					queuedJob.WorkflowJob.Status = github.String("completed")
					err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
					if err != nil {
						logging.Error(pluginCtx, "error updating job in db", "err", err)
					}
					pluginGlobals.JobChannel <- queuedJob
					workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
					return pluginCtx, nil
				}
				logging.Error(pluginCtx, "error getting anka registry vm info", "err", err)
				pluginGlobals.RetryChannel <- "anka_registry_not_running"
				return pluginCtx, nil
			}
			queuedJob.AnkaVM = *ankaRegistryVMInfo
		} else {
			logging.Error(pluginCtx, "anka registry is not running, checking if template is already pulled")
			ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, ankaTemplate)
			if err != nil { // doesn't exist locally
				logging.Error(pluginCtx, "template doesn't exist locally", "err", err)
				workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
				pluginGlobals.RetryChannel <- "anka_template_does_not_exist_locally"
				return pluginCtx, nil
			}
			if ankaShowOutput.Tag != ankaTemplateTag { // exists locally but doesn't match the tag specified in the labels
				logging.Error(pluginCtx, "current anka template tag on the host doesn't match the tag specified in the labels", "ankaShowOutput.Tag", ankaShowOutput.Tag)
				workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
				pluginGlobals.RetryChannel <- "anka_template_tag_does_not_match"
				return pluginCtx, nil
			}
			// Determine CPU and MEM from the template and tag
			templateInfo, err := internalAnka.GetAnkaVmInfo(pluginCtx, ankaTemplate)
			if err != nil {
				logging.Error(pluginCtx, "error getting vm info", "err", err)
				pluginGlobals.RetryChannel <- "anka_template_info_error"
				return pluginCtx, fmt.Errorf("error getting vm info: %s", err.Error())
			}
			queuedJob.AnkaVM = *templateInfo
		}

		err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
		if err != nil {
			logging.Error(pluginCtx, "error updating job in db", "err", err)
		}
	}
	logging.Debug(pluginCtx, "vmInfo", "queuedJob.AnkaVM", queuedJob.AnkaVM)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

	if queuedJob.Action != "paused" { // no need to do this, we already did it when we got the paused job
		// check if the host has enough resources to run this VM
		err = internalAnka.VmHasEnoughHostResources(pluginCtx, queuedJob.AnkaVM)
		if err != nil {
			logging.Warn(pluginCtx, "host does not have enough resources to run vm")
			workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again
			pluginGlobals.RetryChannel <- "not_enough_host_resources"
			return pluginCtx, nil
		}

		// check if, compared to other VMs potentially running, we have enough resources to run this VM
		err = internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM)
		if err != nil {

			// pause all other plugins trying to run
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(true)

			// push to proper queue so other hosts with the same specs can pick it up if available
			queuedJob.PausedOn = pluginConfig.Name
			queuedJob.Action = "paused"
			queuedJobJSON, err := json.Marshal(queuedJob)
			if err != nil {
				logging.Error(pluginCtx, "error marshalling queued job", "err", err)
				pluginGlobals.RetryChannel <- "error_marshalling_queued_job"
				return pluginCtx, nil
			}
			err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
			if err != nil {
				logging.Error(pluginCtx, "error updating job in db", "err", err)
			}
			// check if it's already in the paused queue
			pausedQueuedJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, pausedQueueName)
			if err != nil {
				logging.Error(pluginCtx, "error checking if job is in paused queue", "err", err)
				pluginGlobals.RetryChannel <- "error_checking_if_job_is_in_paused_queue"
				return pluginCtx, nil
			}
			if pausedQueuedJobJSON == "" {
				success, err := databaseContainer.Client.RPush(pluginCtx, pausedQueueName, queuedJobJSON).Result()
				if err != nil {
					logging.Error(pluginCtx, "error pushing job to paused queue", "err", err)
					pluginGlobals.RetryChannel <- "error_pushing_job_to_paused_queue"
					return pluginCtx, nil
				}
				if success == 0 {
					logging.Error(pluginCtx, "error pushing job to paused queue", "err", err)
					pluginGlobals.RetryChannel <- "error_pushing_job_to_paused_queue"
					return pluginCtx, nil
				}
				logging.Info(pluginCtx, "pushed job to paused queue", "queuedJob.WorkflowJob.ID", *queuedJob.WorkflowJob.ID)
			}
			enoughResourcesLoopCount := 0
			logging.Warn(pluginCtx, "cannot run vm yet, waiting for enough resources to be available...")
			for {
				// Check for completed jobs or context cancellation
				select {
				case job := <-pluginGlobals.JobChannel:
					if job.WorkflowJob.Status != nil && (*job.WorkflowJob.Status == "completed" || *job.WorkflowJob.Status == "failed") {
						logging.Info(pluginCtx, *job.WorkflowJob.Status+" job found while waiting for resources")
						// Clean up paused queue entry
						err := internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, pausedQueueName)
						if err != nil {
							// no need to error, as it's not a big deal if it's not there
							logging.Warn(pluginCtx, "error deleting from paused queue", "err", err)
						}
						pluginGlobals.JobChannel <- job // put the job back for cleanup
						return pluginCtx, nil
					}
					// Put the job back if it's not completed
					pluginGlobals.JobChannel <- job
				default:
					// No job in channel, continue with resource checks
				}

				if workerCtx.Err() != nil || pluginCtx.Err() != nil {
					pluginGlobals.RetryChannel <- "context_canceled"
					return pluginCtx, fmt.Errorf("context canceled while waiting for resources")
				}
				time.Sleep(1 * time.Second) // Reduced from 5 seconds to 1 second for faster shutdown response
				if enoughResourcesLoopCount%10 == 0 {
					logging.Warn(pluginCtx, "waiting for enough resources to be available...")
				}
				enoughResourcesLoopCount++
				// check if the job was picked up by another host
				// the other host would have pulled the job from the current host's plugin queue, so we check that
				existingJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, pluginQueueName)
				if err != nil {
					pluginGlobals.RetryChannel <- "error_checking_if_queue_has_job"
					err = internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, pausedQueueName)
					if err != nil {
						logging.Error(pluginCtx, "error deleting from paused queue", "err", err)
					}
					return pluginCtx, nil
				}
				if existingJobJSON == "" { // some other host has taken the job from this host
					logging.Warn(pluginCtx, "job was picked up by another host")
					pluginGlobals.PausedCancellationJobChannel <- queuedJob
					return pluginCtx, nil
				}
				// If there is still a queued job, check if the host has enough resources to run it
				err = internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM)
				if err != nil {
					if enoughResourcesLoopCount%10 == 0 {
						logging.Warn(pluginCtx, "error from vm has enough resources check", "err", err)
					}
					continue
				}
				// remove from paused queue so other hosts won't try to pick it up anymore.
				err = internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, pausedQueueName)
				if err != nil {
					logging.Error(pluginCtx, "error deleting from paused queue", "err", err)
				}
				queuedJob.Action = "in_progress"
				err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
				if err != nil {
					logging.Error(pluginCtx, "error updating job in db", "err", err)
				}
				break
			}
		}
	}

	logging.Info(pluginCtx, "vm has enough resources now to run; starting runner")

	// See if VM Template existing already
	if !pluginConfig.SkipPull {
		noTemplateTagExistsError, templateExistsError := ankaCLI.EnsureVMTemplateExists(workerCtx, pluginCtx, ankaTemplate, ankaTemplateTag)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Error(pluginCtx, "context canceled while ensuring vm template exists")
			pluginGlobals.RetryChannel <- "context_canceled"
			return pluginCtx, nil
		}
		if templateExistsError != nil {
			// DO NOT RETURN AN ERROR TO MAIN. It will cause the other job on this node to be cancelled.
			logging.Warn(pluginCtx, "problem ensuring vm template exists on host", "err", templateExistsError)
			// Check if this is a space-related error that won't resolve on retry
			if strings.Contains(templateExistsError.Error(), "insufficient space on host even after cleanup") {
				logging.Info(pluginCtx, "incrementing queue target index due to insufficient disk space")
				workerGlobals.IncrementQueueTargetIndex() // prevent trying to run this job again on this host
			}
			pluginGlobals.RetryChannel <- "problem_ensuring_vm_template_exists_on_host" // return to queue so another node can pick it up
			return pluginCtx, nil
		}
		if noTemplateTagExistsError != nil {
			logging.Error(pluginCtx, "error ensuring vm template exists on host", "err", noTemplateTagExistsError)
			queuedJob.Action = "cancel"
			err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
			if err != nil {
				logging.Error(pluginCtx, "error updating job in db", "err", err)
			}
			pluginGlobals.JobChannel <- queuedJob
			return pluginCtx, nil
		}
	}

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		// logging.Warn(pluginCtx, "context canceled during vm template check")
		pluginGlobals.RetryChannel <- "context_canceled"
		return pluginCtx, fmt.Errorf("context canceled during vm template check")
	}

	// Get runner registration token
	var runnerRegistration *github.RegistrationToken
	var response *github.Response
	if pluginConfig.Repo != "" {
		pluginCtx, runnerRegistration, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.RegistrationToken, *github.Response, error) {
			runnerRegistration, resp, err := githubClient.Actions.CreateRegistrationToken(context.Background(), pluginConfig.Owner, pluginConfig.Repo)
			return runnerRegistration, resp, err
		})
	} else {
		pluginCtx, runnerRegistration, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.RegistrationToken, *github.Response, error) {
			runnerRegistration, resp, err := githubClient.Actions.CreateOrganizationRegistrationToken(context.Background(), pluginConfig.Owner)
			return runnerRegistration, resp, err
		})
	}
	if err != nil {
		logging.Error(pluginCtx, "error creating registration token", "err", err, "response", response)
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		pluginGlobals.RetryChannel <- "error_creating_registration_token"
		return pluginCtx, nil
	}
	if *runnerRegistration.Token == "" {
		logging.Error(pluginCtx, "registration token is empty; something wrong with github or your service token", "response", response)
		pluginGlobals.RetryChannel <- "registration_token_empty"
		return pluginCtx, nil
	}

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		// logging.Warn(pluginCtx, "context canceled before ObtainAnkaVM")
		pluginGlobals.RetryChannel <- "context_canceled"
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
			// logging.Error(pluginCtx, "error marshalling vm to json", "err", wrappedVmErr)
			err = ankaCLI.AnkaDelete(workerCtx, pluginCtx, vm.Name)
			if err != nil {
				logging.Error(pluginCtx, "error deleting vm", "err", err)
			}
			pluginGlobals.RetryChannel <- "error_marshalling_vm_to_json"
			return pluginCtx, fmt.Errorf("error marshalling vm to json: %s", wrappedVmErr.Error())
		}
	}
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		err = ankaCLI.AnkaDelete(workerCtx, pluginCtx, vm.Name)
		if err != nil {
			logging.Error(pluginCtx, "error deleting vm", "err", err)
		}
		return pluginCtx, fmt.Errorf("context canceled after ObtainAnkaVM")
	}

	// free up other plugins to run
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)

	pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

	dbErr := databaseContainer.Client.RPush(pluginCtx, pluginQueueName, wrappedVmJSON).Err()
	if dbErr != nil {
		// logging.Error(pluginCtx, "error pushing vm data to database", "err", dbErr)
		pluginGlobals.RetryChannel <- "error_pushing_vm_data_to_database"
		return pluginCtx, fmt.Errorf("error pushing vm data to database: %s", dbErr.Error())
	}
	if err != nil {
		// this is thrown, for example, when there is no capacity on the host
		// we must be sure to create the DB entry so cleanup happens properly
		logging.Error(pluginCtx, "error obtaining anka vm", "err", err)
		pluginGlobals.RetryChannel <- "error_obtaining_anka_vm"
		return pluginCtx, nil
	}

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		// logging.Warn(pluginCtx, "context canceled after ObtainAnkaVM")
		pluginGlobals.RetryChannel <- "context_canceled"
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
		// logging.Error(pluginCtx, "must include install-runner.bash, register-runner.bash, and start-runner.bash in "+globals.PluginsPath+"/handlers/github/", "err", err)
		pluginGlobals.RetryChannel <- "cancel"
		err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
		if err != nil {
			logging.Error(pluginCtx, "error updating job in db", "err", err)
		}
		pluginGlobals.JobChannel <- queuedJob
		return pluginCtx, fmt.Errorf("must include install-runner.bash, register-runner.bash, and start-runner.bash in %s/handlers/github/", workerGlobals.PluginsPath)
	}

	// Copy runner scripts to VM
	logging.Debug(pluginCtx, "copying install-runner.bash, register-runner.bash, and start-runner.bash to vm")
	err = ankaCLI.AnkaCopyIntoVM(workerCtx, pluginCtx,
		queuedJob.AnkaVM.Name,
		installRunnerPath,
		registerRunnerPath,
		startRunnerPath,
	)
	if err != nil {
		// logging.Error(pluginCtx, "error executing anka copy", "err", err)
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		pluginGlobals.RetryChannel <- "error_executing_anka_copy"
		return pluginCtx, fmt.Errorf("error executing anka copy: %s", err.Error())
	}
	select {
	case <-pluginGlobals.JobChannel:
		logging.Warn(pluginCtx, "completed job found before installing runner")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before install runner")
		return pluginCtx, nil
	default:
	}

	// Install runner
	logging.Debug(pluginCtx, "installing github runner inside of vm")
	installRunnerErr = ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./install-runner.bash")
	if installRunnerErr != nil {
		logging.Error(pluginCtx, "error executing install-runner.bash", "err", installRunnerErr)
		pluginGlobals.RetryChannel <- "error_executing_install_runner_bash"
		return pluginCtx, nil // do not return error here; curl can fail and we need to retry
	}
	// Register runner
	select {
	case <-pluginGlobals.JobChannel:
		logging.Info(pluginCtx, "completed job found before registering runner")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before register runner")
		return pluginCtx, nil
	default:
	}
	logging.Debug(pluginCtx, "registering github runner inside of vm", "queuedJob", queuedJob)
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
		// logging.Error(pluginCtx, "error executing register-runner.bash", "err", registerRunnerErr)
		pluginGlobals.RetryChannel <- "error_executing_register_runner_bash"
		return pluginCtx, fmt.Errorf("error executing register-runner.bash: %s", registerRunnerErr.Error())
	}
	// make sure we can clean up properly if we interrupt early
	queuedJob.Action = "registered"
	err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
	if err != nil {
		logging.Error(pluginCtx, "error updating job in db", "err", err)
	}
	// needed to clean up registered runners
	defer removeSelfHostedRunner(workerCtx, pluginCtx, pluginQueueName)
	// Start runner
	select {
	case <-pluginGlobals.JobChannel:
		logging.Info(pluginCtx, "completed job found before starting runner")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before start runner")
		return pluginCtx, nil
	default:
	}
	logging.Debug(pluginCtx, "starting github runner inside of vm")
	startRunnerErr = ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./start-runner.bash")
	if startRunnerErr != nil {
		// logging.Error(pluginCtx, "error executing start-runner.bash", "err", startRunnerErr)
		pluginGlobals.RetryChannel <- "error_executing_start_runner_bash"
		return pluginCtx, fmt.Errorf("error executing start-runner.bash: %s", startRunnerErr.Error())
	}

	select {
	case <-pluginGlobals.JobChannel:
		logging.Info(pluginCtx, "completed job found before jobCompleted checks")
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before jobCompleted checks")
		return pluginCtx, nil
	default:
	}

	logging.Info(pluginCtx, "finished preparing anka VM with actions runner")
	queuedJob.Action = "in_progress"
	err = internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, &queuedJob)
	if err != nil {
		logging.Error(pluginCtx, "error updating job in db", "err", err)
	}

	// Watch for job completion
	pluginCtx, err = watchJobStatus(
		workerCtx,
		pluginCtx,
		pluginQueueName,
		mainInProgressQueueName,
	)
	if err != nil {
		logging.Error(pluginCtx, "error watching for job completion", "err", err)
		return pluginCtx, err
	}

	return pluginCtx, nil
}

// removeSelfHostedRunner handles removing a registered runner if the registered runner was orphaned somehow
// it's extra safety should the runner not be registered with --ephemera
func removeSelfHostedRunner(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
) {
	var err error
	var runnersList *github.Runners
	var response *github.Response
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin from context", "err", err)
	}
	githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting github client from context", "err", err)
	}

	queuedJob, err := internalGithub.GetJobFromQueue(pluginCtx, pluginQueueName)
	if err != nil {
		logging.Error(pluginCtx, "error getting job from queue", "err", err)
	}

	// we don't use cancelled here since the registration will auto unregister after the job finishes
	if (queuedJob.WorkflowJob.Conclusion != nil && *queuedJob.WorkflowJob.Conclusion == "failure") ||
		(queuedJob.Action != "" && queuedJob.Action == "remove_self_hosted_runner") {
		logging.Info(pluginCtx, "removeSelfHostedRunner | checking for runners to delete", "queuedJob", queuedJob)
		if pluginConfig.Repo != "" {
			pluginCtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Runners, *github.Response, error) {
				runnersList, resp, err := githubClient.Actions.ListRunners(context.Background(), pluginConfig.Owner, pluginConfig.Repo, &github.ListRunnersOptions{})
				return runnersList, resp, err
			})
		} else {
			pluginCtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Runners, *github.Response, error) {
				runnersList, resp, err := githubClient.Actions.ListOrganizationRunners(context.Background(), pluginConfig.Owner, &github.ListRunnersOptions{})
				return runnersList, resp, err
			})
		}
		if err != nil {
			logging.Error(pluginCtx, "error executing githubClient.Actions.ListRunners", "err", err, "response", response)
			return
		}
		if len(runnersList.Runners) == 0 {
			logging.Debug(pluginCtx, "no runners found to delete (not an error)")
		} else {
			// found := false
			for _, runner := range runnersList.Runners {
				if *runner.Name == queuedJob.AnkaVM.Name {

					// found = true
					/*
						UPDATE as of 0.14.2: We can't really prevent this from happening. Since the job can be retried, we don't want to cancel it.
						We have to cancel the workflow run before we can remove the runner.
						[11:12:53.736] ERROR: error executing githubClient.Actions.RemoveRunner {
						"ankaTemplate": "d792c6f6-198c-470f-9526-9c998efe7ab4",
						"ankaTemplateTag": "(using latest)",
						"err": "DELETE https://api.github.com/repos/veertuinc/anklet/actions/runners/142: 422 Bad request - Runner \"anklet-vm-\u003cuuid\u003e\" is still running a job\" []",
					*/
					if queuedJob.Action != "remove_self_hosted_runner" { // no need to cancel if we're just removing the runner
						err := sendCancelWorkflowRun(workerCtx, pluginCtx, queuedJob)
						if err != nil {
							logging.Error(pluginCtx, "error sending cancel workflow run", "err", err)
							return
						}
					}
					if pluginConfig.Repo != "" {
						pluginCtx, _, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
							response, err := githubClient.Actions.RemoveRunner(context.Background(), pluginConfig.Owner, pluginConfig.Repo, *runner.ID)
							return response, nil, err
						})
					} else {
						pluginCtx, _, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
							response, err := githubClient.Actions.RemoveOrganizationRunner(context.Background(), pluginConfig.Owner, *runner.ID)
							return response, nil, err
						})
					}
					if err != nil {
						logging.Error(pluginCtx, "error executing githubClient.Actions.RemoveRunner", "err", err)
						return
					} else {
						logging.Info(pluginCtx, "successfully removed runner")
					}
					break
				}
			}
			// if !found {
			// 	logging.Info(pluginCtx, "no matching runner found")
			// }
		}
	}
}
