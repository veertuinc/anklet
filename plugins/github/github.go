package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v58/github"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	dbFunctions "github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
)

type WorkflowRunJobDetail struct {
	Job             github.WorkflowJob
	WorkflowRunName string
	AnkaTemplate    string
	AnkaTemplateTag string
	RunID           string
	UniqueID        string
}

func exists_in_array(array_to_search_in []string, desired []string) bool {
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

func extractLabelValue(labels []string, prefix string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix)
		}
	}
	return ""
}

// https://github.com/gofri/go-github-ratelimit has yet to support primary rate limits, so we have to do it ourselves.
func ExecuteGitHubClientFunction[T any](ctx context.Context, logger *slog.Logger, executeFunc func() (*T, *github.Response, error)) (*T, *github.Response, error) {
	result, response, err := executeFunc()
	if response != nil && response.Rate.Remaining <= 10 { // handle primary rate limiting
		sleepDuration := time.Until(response.Rate.Reset.Time) + time.Second // Adding a second to ensure we're past the reset time
		logger.WarnContext(ctx, "GitHub API rate limit exceeded, sleeping until reset", "resetTime", response.Rate.Reset.Time.Format(time.RFC3339))
		select {
		case <-time.After(sleepDuration):
			return ExecuteGitHubClientFunction(ctx, logger, executeFunc) // Retry the function after waiting
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if err != nil &&
		err.Error() != "context canceled" &&
		!strings.Contains(err.Error(), "try again later") {
		logger.Error("error executing GitHub client function: " + err.Error())
		return nil, nil, err
	}
	return result, response, nil
}

func setLoggingContext(ctx context.Context, workflowRunJob WorkflowRunJobDetail) context.Context {
	ctx = logging.AppendCtx(ctx, slog.String("workflowName", *workflowRunJob.Job.WorkflowName))
	ctx = logging.AppendCtx(ctx, slog.String("workflowRunName", workflowRunJob.WorkflowRunName))
	ctx = logging.AppendCtx(ctx, slog.Int64("workflowRunId", *workflowRunJob.Job.RunID))
	ctx = logging.AppendCtx(ctx, slog.Int64("workflowJobId", *workflowRunJob.Job.ID))
	ctx = logging.AppendCtx(ctx, slog.String("workflowJobName", *workflowRunJob.Job.Name))
	ctx = logging.AppendCtx(ctx, slog.String("uniqueId", workflowRunJob.UniqueID))
	ctx = logging.AppendCtx(ctx, slog.String("ankaTemplate", workflowRunJob.AnkaTemplate))
	ctx = logging.AppendCtx(ctx, slog.String("ankaTemplateTag", workflowRunJob.AnkaTemplateTag))
	ctx = logging.AppendCtx(ctx, slog.String("jobURL", *workflowRunJob.Job.HTMLURL))
	return ctx
}

func getWorkflowRunJobs(ctx context.Context, logger *slog.Logger) ([]WorkflowRunJobDetail, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("context canceled before getWorkflowRunJobs")
	}
	githubClient := internalGithub.GetGitHubClientFromContext(ctx)
	service := config.GetServiceFromContext(ctx)
	var allWorkflowRunJobDetails []WorkflowRunJobDetail
	// WORKFLOWS
	workflows, _, err := ExecuteGitHubClientFunction[*github.Workflows](ctx, logger, func() (**github.Workflows, *github.Response, error) {
		workflows, resp, err := githubClient.Actions.ListWorkflows(context.Background(), service.Owner, service.Repo, &github.ListOptions{})
		return &workflows, resp, err
	})
	if ctx.Err() != nil {
		logger.WarnContext(ctx, "context canceled during workflows listing")
		return []WorkflowRunJobDetail{}, errors.New("context canceled during workflows listing")
	}
	if err != nil {
		logger.ErrorContext(ctx, "error executing githubClient.Actions.ListWorkflows", "err", err)
		return []WorkflowRunJobDetail{}, errors.New("error executing githubClient.Actions.ListWorkflows")
	}
	for _, workflow := range (*workflows).Workflows {
		if *workflow.State == "active" {
			// WORKFLOW RUNS
			workflow_runs, _, err := ExecuteGitHubClientFunction[*github.WorkflowRuns](ctx, logger, func() (**github.WorkflowRuns, *github.Response, error) {
				workflow_runs, resp, err := githubClient.Actions.ListWorkflowRunsByID(context.Background(), service.Owner, service.Repo, *workflow.ID, &github.ListWorkflowRunsOptions{
					// ListOptions: github.ListOptions{PerPage: 30},
					Status: "queued",
				})
				return &workflow_runs, resp, err // Adjusted to return the direct result
			})
			if err != nil {
				if strings.Contains(err.Error(), "context canceled") {
					logger.WarnContext(ctx, "context canceled during githubClient.Actions.ListWorkflowRunsByID", "err", err)
				} else {
					logger.ErrorContext(ctx, "error executing githubClient.Actions.ListWorkflowRunsByID", "err", err)
				}
				return []WorkflowRunJobDetail{}, errors.New("error executing githubClient.Actions.ListWorkflowRunsByID")
			}
			for _, workflowRun := range (*workflow_runs).WorkflowRuns {
				workflowRunJobs, _, err := ExecuteGitHubClientFunction[github.Jobs](ctx, logger, func() (*github.Jobs, *github.Response, error) {
					workflowRunJobs, resp, err := githubClient.Actions.ListWorkflowJobs(context.Background(), service.Owner, service.Repo, *workflowRun.ID, &github.ListWorkflowJobsOptions{})
					return workflowRunJobs, resp, err
				})
				if err != nil {
					if strings.Contains(err.Error(), "context canceled") {
						logger.WarnContext(ctx, "context canceled during githubClient.Actions.ListWorkflowJobs", "err", err)
					} else {
						logger.ErrorContext(ctx, "error executing githubClient.Actions.ListWorkflowJobs", "err", err)
					}
					return []WorkflowRunJobDetail{}, errors.New("error executing githubClient.Actions.ListWorkflowJobs")
				}
				for _, job := range workflowRunJobs.Jobs {
					if *job.Status == "queued" { // I don't know why, but we'll get completed jobs back in the list
						if exists_in_array(job.Labels, []string{"self-hosted", "anka"}) {
							ctx = setLoggingContext(ctx, WorkflowRunJobDetail{
								Job:             *job,
								WorkflowRunName: *workflowRun.Name,
							})

							// this ensures that jobs in the same workspace don't compete for the same runner
							runID := extractLabelValue(job.Labels, "run-id:")
							if runID == "" {
								logger.WarnContext(ctx, "run-id label not found or empty; something wrong with your workflow yaml")
								continue
							}
							if runID != strconv.FormatInt(*job.RunID, 10) { // make sure the user set it properly
								logger.WarnContext(ctx, "run-id label does not match the job's run ID; potential misconfiguration in workflow yaml")
								continue
							}

							// get the unique unique-id for this job
							// this ensures that multiple jobs in the same workflow run don't compete for the same runner
							uniqueID := extractLabelValue(job.Labels, "unique-id:")
							if uniqueID == "" {
								logger.WarnContext(ctx, "unique-id label not found or empty; something wrong with your workflow yaml")
								continue
							}

							ankaTemplate := extractLabelValue(job.Labels, "anka-template:")
							if ankaTemplate == "" {
								logger.WarnContext(ctx, "warning: unable to find Anka Template specified in labels - skipping")
								continue
							}
							ankaTemplateTag := extractLabelValue(job.Labels, "anka-template-tag:")
							if ankaTemplateTag == "" {
								ankaTemplateTag = "(using latest)"
							}

							// if a node is pulling, the job doesn't change from queued, so let's do a check to see if a node picked it up or not
							exists, err := dbFunctions.CheckIfKeyExists(ctx, fmt.Sprintf("%s:%s", runID, uniqueID))
							if err != nil {
								if strings.Contains(err.Error(), "context canceled") {
									logger.WarnContext(ctx, "context was canceled while checking if key exists in database", "err", err)
								} else {
									logger.ErrorContext(ctx, "error checking if key exists in database", "err", err)
								}
								return []WorkflowRunJobDetail{}, errors.New("error checking if key exists in database")
							}

							if !exists {
								allWorkflowRunJobDetails = append(allWorkflowRunJobDetails, WorkflowRunJobDetail{
									Job:             *job,
									WorkflowRunName: *workflowRun.Name,
									AnkaTemplate:    ankaTemplate,
									AnkaTemplateTag: ankaTemplateTag,
									RunID:           runID,
									UniqueID:        uniqueID,
								})
							}
						}
					}
				}
			}
		}
	}

	sort.Slice(allWorkflowRunJobDetails, func(i, j int) bool {
		if allWorkflowRunJobDetails[i].Job.CreatedAt.Equal(*allWorkflowRunJobDetails[j].Job.CreatedAt) {
			return *allWorkflowRunJobDetails[i].Job.Name < *allWorkflowRunJobDetails[j].Job.Name
		}
		return allWorkflowRunJobDetails[i].Job.CreatedAt.Time.Before(allWorkflowRunJobDetails[j].Job.CreatedAt.Time)
	})

	return allWorkflowRunJobDetails, nil
}

func Run(ctx context.Context, logger *slog.Logger) {

	hostHasVmCapacity := anka.HostHasVmCapacity(ctx)
	if !hostHasVmCapacity {
		return
	}

	service := config.GetServiceFromContext(ctx)

	if service.Token == "" {
		logging.Panic(ctx, "token is not set in ankalet.yaml:services:"+service.Name+":token")
	}
	if service.Owner == "" {
		logging.Panic(ctx, "owner is not set in ankalet.yaml:services:"+service.Name+":owner")
	}
	if service.Repo == "" {
		logging.Panic(ctx, "repo is not set in anklet.yaml:services:"+service.Name+":repo")
	}
	ctx = logging.AppendCtx(ctx, slog.String("repo", service.Repo))
	ctx = logging.AppendCtx(ctx, slog.String("owner", service.Owner))

	repositoryURL := fmt.Sprintf("https://github.com/%s/%s", service.Owner, service.Repo)

	// obtain all queued workflow runs and jobs
	allWorkflowRunJobDetails, err := getWorkflowRunJobs(ctx, logger)
	if err != nil {
		return
	}

	// simplifiedWorkflowRuns := make([]map[string]interface{}, 0)
	// for _, workflowRunJob := range allWorkflowRunJobDetails {
	// 	simplifiedRun := map[string]interface{}{
	// 		"name":              workflowRunJob.Job.Name,
	// 		"created_at":        workflowRunJob.Job.CreatedAt,
	// 		"workflow_name":     workflowRunJob.Job.WorkflowName,
	// 		"workflow_run_name": workflowRunJob.WorkflowRunName,
	// 		"run_id":            workflowRunJob.Job.RunID,
	// 		"unique_id":         workflowRunJob.UniqueID,
	// 		"html_url":          workflowRunJob.Job.HTMLURL,
	// 		"labels":            workflowRunJob.Job.Labels,
	// 		"status":            workflowRunJob.Job.Status,
	// 	}
	// 	simplifiedWorkflowRuns = append(simplifiedWorkflowRuns, simplifiedRun)
	// }
	// allWorkflowRunJobsJSON, _ := json.MarshalIndent(simplifiedWorkflowRuns, "", "  ")
	// fmt.Printf("%s\n", allWorkflowRunJobsJSON)

	// Loop over all items, so we don't have to re-request the whole list of queued jobs if one is already running on another host
	for _, workflowRunJob := range allWorkflowRunJobDetails {
		ctx = setLoggingContext(ctx, workflowRunJob)

		// Check if the job is already running, and ensure in DB to prevent other runners from getting it
		uniqueKey := fmt.Sprintf("%s:%s", workflowRunJob.RunID, workflowRunJob.UniqueID)
		ctx = dbFunctions.UpdateUniqueRunKey(ctx, uniqueKey)
		if already, err := dbFunctions.CheckIfKeyExists(ctx, uniqueKey); err != nil {
			logger.ErrorContext(ctx, "error checking if already in db", "err", err)
			return
		} else if already {
			// logger.DebugContext(ctx, "job already running, skipping")
			// this would cause a double run problem if a job finished on hostA and hostB had an array of workflowRunJobs with queued still for the same job
			// we get the latest workflow run jobs each run to prevent this
			return
		} else if !already {
			added, err := dbFunctions.AddUniqueRunKey(ctx)
			if added && err != nil {
				logger.DebugContext(ctx, "unique key already in db")
				continue // go to next item so we don't have to query for all running jobs again if another host already picked it up
			}
			if !added && err != nil {
				logger.ErrorContext(ctx, "error adding unique run key", "err", err)
				return
			}
		}
		defer dbFunctions.RemoveUniqueKeyFromDB(ctx)

		logger.InfoContext(ctx, "handling anka workflow run job")

		// get anka CLI
		ankaCLI := anka.GetAnkaCLIFromContext(ctx)

		// See if VM Template existing already
		templateTagExistsError := ankaCLI.EnsureVMTemplateExists(ctx, workflowRunJob.AnkaTemplate, workflowRunJob.AnkaTemplateTag)
		if templateTagExistsError != nil {
			logger.DebugContext(ctx, "error ensuring vm template exists", "err", templateTagExistsError)
			return
		}

		logger.DebugContext(ctx, "handling job")

		// Get runner registration token
		githubClient := internalGithub.GetGitHubClientFromContext(ctx)
		repoRunnerRegistration, response, err := ExecuteGitHubClientFunction[github.RegistrationToken](ctx, logger, func() (*github.RegistrationToken, *github.Response, error) {
			repoRunnerRegistration, resp, err := githubClient.Actions.CreateRegistrationToken(context.Background(), service.Owner, service.Repo)
			return repoRunnerRegistration, resp, err
		})
		if err != nil {
			logger.ErrorContext(ctx, "error creating registration token", "err", err, "response", response)
			return
		}
		if *repoRunnerRegistration.Token == "" {
			logger.ErrorContext(ctx, "registration token is empty; something wrong with github or your service token", "response", response)
			return
		}

		if ctx.Err() != nil {
			logger.WarnContext(ctx, "context canceled before ObtainAnkaVM")
			return
		}

		// Obtain Anka VM (and name)
		ctx, vm, err := ankaCLI.ObtainAnkaVM(ctx, workflowRunJob.AnkaTemplate)
		defer ankaCLI.AnkaDelete(ctx, vm)
		if err != nil {
			logger.ErrorContext(ctx, "error obtaining anka vm", "err", err)
			return
		}

		// Install runner
		globals := config.GetGlobalsFromContext(ctx)
		logger.InfoContext(ctx, "installing github runner inside of vm")
		err = ankaCLI.AnkaCopy(ctx,
			globals.PluginsPath+"/github/install-runner.bash",
			globals.PluginsPath+"/github/register-runner.bash",
			globals.PluginsPath+"/github/start-runner.bash",
		)
		if err != nil {
			logger.ErrorContext(ctx, "error executing anka copy", "err", err)
			return
		}

		if ctx.Err() != nil {
			logger.WarnContext(ctx, "context canceled before install runner")
			return
		}
		ankaCLI.AnkaRun(ctx,
			"./install-runner.bash",
		)
		// Register runner
		if ctx.Err() != nil {
			logger.WarnContext(ctx, "context canceled before register runner")
			return
		}
		ankaCLI.AnkaRun(ctx,
			"./register-runner.bash",
			vm.Name, *repoRunnerRegistration.Token, repositoryURL, strings.Join(workflowRunJob.Job.Labels, ","),
		)
		defer removeSelfHostedRunner(ctx, *vm, *workflowRunJob.Job.RunID)
		// Install and Start runner
		if ctx.Err() != nil {
			logger.WarnContext(ctx, "context canceled before start runner")
			return
		}
		ankaCLI.AnkaRun(ctx, "./start-runner.bash")
		if ctx.Err() != nil {
			logger.WarnContext(ctx, "context canceled before jobCompleted checks")
			return
		}

		// Watch for job completion
		jobCompleted := false
		logCounter := 0
		for !jobCompleted {
			if ctx.Err() != nil {
				logger.WarnContext(ctx, "context canceled while watching for job completion")
				break
			}
			currentJob, response, err := ExecuteGitHubClientFunction[github.WorkflowJob](ctx, logger, func() (*github.WorkflowJob, *github.Response, error) {
				currentJob, resp, err := githubClient.Actions.GetWorkflowJobByID(context.Background(), service.Owner, service.Repo, *workflowRunJob.Job.ID)
				return currentJob, resp, err
			})
			if err != nil {
				logger.ErrorContext(ctx, "error executing githubClient.Actions.GetWorkflowJobByID", "err", err, "response", response)
				return
			}
			if *currentJob.Status == "completed" {
				jobCompleted = true
				ctx = logging.AppendCtx(ctx, slog.String("conclusion", *currentJob.Conclusion))
				logger.InfoContext(ctx, "job completed", "job_id", *workflowRunJob.Job.ID)
			} else if logCounter%2 == 0 {
				if ctx.Err() != nil {
					logger.WarnContext(ctx, "context canceled during job status check")
					return
				}
				logger.InfoContext(ctx, "job still in progress", "job_id", *workflowRunJob.Job.ID)
				time.Sleep(5 * time.Second) // Wait before checking the job status again
			}
			logCounter++
		}
		// Important return!
		// only handle a single job for this service, then return so we get fresh context
		// If we don't, we can pick up a job that has already run on another host but is still in the list of jobs we queried at the beginning
		return
	}
}

// removeSelfHostedRunner handles removing a registered runner if the registered runner was orphaned somehow
// it's extra safety should the runner not be registered with --ephemeral
func removeSelfHostedRunner(ctx context.Context, vm anka.VM, workflowRunID int64) {
	logger := logging.GetLoggerFromContext(ctx)
	service := config.GetServiceFromContext(ctx)
	githubClient := internalGithub.GetGitHubClientFromContext(ctx)
	runnersList, response, err := ExecuteGitHubClientFunction[github.Runners](ctx, logger, func() (*github.Runners, *github.Response, error) {
		runnersList, resp, err := githubClient.Actions.ListRunners(context.Background(), service.Owner, service.Repo, &github.ListOptions{})
		return runnersList, resp, err
	})
	if err != nil {
		logger.ErrorContext(ctx, "error executing githubClient.Actions.ListRunners", "err", err, "response", response)
		return
	}
	if len(runnersList.Runners) == 0 {
		logger.DebugContext(ctx, "no runners found to delete (not an error)")
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
				cancelSent := false
				for {
					workflowRun, _, err := ExecuteGitHubClientFunction[github.WorkflowRun](ctx, logger, func() (*github.WorkflowRun, *github.Response, error) {
						workflowRun, resp, err := githubClient.Actions.GetWorkflowRunByID(context.Background(), service.Owner, service.Repo, workflowRunID)
						return workflowRun, resp, err
					})
					if err != nil {
						logger.ErrorContext(ctx, "error getting workflow run by ID", "err", err)
						return
					}
					if *workflowRun.Status == "completed" || (workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") {
						break
					} else {
						logger.WarnContext(ctx, "workflow run is still active... waiting for cancellation so we can clean up the runner...", "workflow_run_id", workflowRunID)
						if !cancelSent { // this has to happen here so that it doesn't error with "409 Cannot cancel a workflow run that is completed. " if the job is already cancelled
							cancelResponse, _, cancelErr := ExecuteGitHubClientFunction[github.Response](ctx, logger, func() (*github.Response, *github.Response, error) {
								resp, err := githubClient.Actions.CancelWorkflowRunByID(context.Background(), service.Owner, service.Repo, workflowRunID)
								return resp, nil, err
							})
							// don't use cancelResponse.Response.StatusCode or else it'll error with SIGSEV
							if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
								logger.ErrorContext(ctx, "error executing githubClient.Actions.CancelWorkflowRunByID", "err", cancelErr, "response", cancelResponse)
								break
							}
							cancelSent = true
						}
						time.Sleep(10 * time.Second)
					}
				}
				_, _, err = ExecuteGitHubClientFunction[github.Response](ctx, logger, func() (*github.Response, *github.Response, error) {
					response, err := githubClient.Actions.RemoveRunner(context.Background(), service.Owner, service.Repo, *runner.ID)
					return response, nil, err
				})
				if err != nil {
					logger.ErrorContext(ctx, "error executing githubClient.Actions.RemoveRunner", "err", err)
					return
				} else {
					logger.InfoContext(ctx, "successfully removed runner")
				}
				break
			}
		}
		// if !found {
		// 	logger.InfoContext(ctx, "no matching runner found")
		// }
	}
}
