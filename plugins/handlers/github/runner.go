package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/veertuinc/anklet/internal/config"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

func (h *Handler) createRegistrationToken(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *github.Client,
) (context.Context, *github.RegistrationToken, *github.Response, error) {
	pluginConfig := h.deps.PluginConfig
	switch pluginConfig.GitHubScope() {
	case config.GitHubScopeRepository:
		return internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.RegistrationToken, *github.Response, error) {
			return githubClient.Actions.CreateRegistrationToken(pluginCtx, pluginConfig.Owner, pluginConfig.Repo)
		})
	case config.GitHubScopeEnterprise:
		return internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.RegistrationToken, *github.Response, error) {
			return githubClient.Enterprise.CreateRegistrationToken(pluginCtx, pluginConfig.Enterprise)
		})
	default:
		return internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.RegistrationToken, *github.Response, error) {
			return githubClient.Actions.CreateOrganizationRegistrationToken(pluginCtx, pluginConfig.Owner)
		})
	}
}

func sendCancelWorkflowRun(
	workerCtx context.Context,
	pluginCtx context.Context,
	metricsData *metrics.MetricsDataLock,
	queuedJob internalGithub.QueueJob,
) error {
	if queuedJob.WorkflowJob.RunID == nil {
		return fmt.Errorf("cannot cancel workflow run: run_id is nil")
	}
	githubClient, err := internalGithub.GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		return err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	if metricsData == nil {
		metricsData, err = metrics.GetMetricsDataFromContext(workerCtx)
		if err != nil {
			return err
		}
	}
	jobOwner, jobRepo, err := queuedJob.RepositoryOwnerAndName()
	if err != nil {
		return err
	}
	if wrapper, wrapperErr := internalGithub.GetGitHubClientWrapperFromContext(pluginCtx); wrapperErr == nil {
		orgClient, orgErr := wrapper.ClientForOrganization(pluginCtx, jobOwner)
		if orgErr != nil {
			return orgErr
		}
		githubClient = orgClient
	}

	cancelSent := false
	for {
		newPluginCtx, workflowRun, _, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.WorkflowRun, *github.Response, error) {
			return githubClient.Actions.GetWorkflowRunByID(pluginCtx, jobOwner, jobRepo, *queuedJob.WorkflowJob.RunID)
		})
		if err != nil {
			logging.Error(newPluginCtx, "error getting workflow run by ID", "error", err)
			return err
		}
		pluginCtx = newPluginCtx
		if (workflowRun.Status != nil && *workflowRun.Status == "completed") ||
			(workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") ||
			cancelSent {
			metricsData.IncrementTotalCanceledRunsSinceStart(workerCtx, pluginCtx)
			jobURL := ""
			if queuedJob.WorkflowJob.HTMLURL != nil {
				jobURL = *queuedJob.WorkflowJob.HTMLURL
			}
			if updateErr := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
				PluginBase: metrics.PluginBase{
					Name: pluginConfig.Name,
				},
				LastCanceledRun:       time.Now(),
				LastCanceledRunJobUrl: jobURL,
			}); updateErr != nil {
				return fmt.Errorf("error updating plugin metrics: %w", updateErr)
			}
			break
		}

		logging.Warn(pluginCtx, "workflow run is still active... waiting for cancellation so we can clean up...", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
		if !cancelSent {
			newPluginCtx, cancelResponse, _, cancelErr := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
				resp, reqErr := githubClient.Actions.CancelWorkflowRunByID(pluginCtx, jobOwner, jobRepo, *queuedJob.WorkflowJob.RunID)
				return resp, resp, reqErr
			})
			if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
				if strings.Contains(cancelErr.Error(), "Resource not accessible by integration") {
					cancelErr = fmt.Errorf("%w (GitHub App needs Repository permissions Actions + Administration Read and write on org %s, and must be installed on that org/repos; accept any pending permission update)", cancelErr, jobOwner)
				}
				logging.Error(newPluginCtx, "error executing githubClient.Actions.CancelWorkflowRunByID", "error", cancelErr, "response", cancelResponse, "owner", jobOwner, "repo", jobRepo)
				return cancelErr
			}
			pluginCtx = newPluginCtx
			cancelSent = true
			logging.Warn(pluginCtx, "sent cancel workflow run", "workflow_run_id", *queuedJob.WorkflowJob.RunID)
		}

		timer := time.NewTimer(10 * time.Second)
		select {
		case <-timer.C:
		case <-workerCtx.Done():
			timer.Stop()
			logging.Warn(pluginCtx, "context canceled while waiting for workflow cancellation")
			return fmt.Errorf("context canceled while waiting for workflow cancellation")
		case <-pluginCtx.Done():
			timer.Stop()
			logging.Warn(pluginCtx, "context canceled while waiting for workflow cancellation")
			return fmt.Errorf("context canceled while waiting for workflow cancellation")
		}
	}
	return nil
}

// removeSelfHostedRunner removes an orphaned runner registration on GitHub if needed.
func (h *Handler) removeSelfHostedRunner(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
) {
	cleanupAPICtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cleanupAPICtx = h.deps.WithContext(cleanupAPICtx)
	if wrapper, err := internalGithub.GetGitHubClientWrapperFromContext(pluginCtx); err == nil {
		cleanupAPICtx = context.WithValue(cleanupAPICtx, config.ContextKey("githubwrapperclient"), wrapper)
	}

	pluginConfig := h.deps.PluginConfig
	githubClient, err := internalGithub.GetGitHubClientFromContext(cleanupAPICtx)
	if err != nil {
		logging.Error(cleanupAPICtx, "error getting github client from context", "error", err)
		return
	}

	queuedJob, err := internalGithub.GetJobFromQueue(cleanupAPICtx, pluginQueueName)
	if err != nil {
		logging.Error(cleanupAPICtx, "error getting job from queue", "error", err)
		return
	}

	logging.Debug(cleanupAPICtx, "running removeSelfHostedRunner")

	shouldCheck := (queuedJob.WorkflowJob.Conclusion != nil && *queuedJob.WorkflowJob.Conclusion == "failure") ||
		queuedJob.Action == "remove_self_hosted_runner"
	if !shouldCheck {
		return
	}

	logging.Info(cleanupAPICtx, "removeSelfHostedRunner | checking for runners to delete", "queuedJob", queuedJob)
	var runnersList *github.Runners
	var response *github.Response
	switch pluginConfig.GitHubScope() {
	case config.GitHubScopeRepository:
		cleanupAPICtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, cleanupAPICtx, func() (*github.Runners, *github.Response, error) {
			return githubClient.Actions.ListRunners(cleanupAPICtx, pluginConfig.Owner, pluginConfig.Repo, &github.ListRunnersOptions{})
		})
	case config.GitHubScopeEnterprise:
		cleanupAPICtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, cleanupAPICtx, func() (*github.Runners, *github.Response, error) {
			return githubClient.Enterprise.ListRunners(cleanupAPICtx, pluginConfig.Enterprise, &github.ListRunnersOptions{})
		})
	default:
		cleanupAPICtx, runnersList, response, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, cleanupAPICtx, func() (*github.Runners, *github.Response, error) {
			return githubClient.Actions.ListOrganizationRunners(cleanupAPICtx, pluginConfig.Owner, &github.ListRunnersOptions{})
		})
	}
	if err != nil {
		logging.Error(cleanupAPICtx, "error executing githubClient.Actions.ListRunners", "error", err, "response", response)
		return
	}
	if runnersList == nil || len(runnersList.Runners) == 0 {
		logging.Debug(cleanupAPICtx, "no runners found to delete (not an error)")
		return
	}

	for _, runner := range runnersList.Runners {
		if runner == nil || runner.Name == nil || runner.ID == nil {
			continue
		}
		if *runner.Name != queuedJob.AnkaVM.Name {
			continue
		}
		var removeErr error
		cleanupAPICtx, removeErr = removeRunnerByScope(workerCtx, cleanupAPICtx, githubClient, pluginConfig, *runner.ID)
		if removeErr != nil && strings.Contains(removeErr.Error(), "is currently running a job and cannot be deleted") {
			logging.Info(cleanupAPICtx, "runner is currently running a job, canceling workflow before removal")
			if cancelErr := sendCancelWorkflowRun(workerCtx, cleanupAPICtx, h.deps.Metrics, queuedJob); cancelErr != nil {
				logging.Error(cleanupAPICtx, "error sending cancel workflow run", "error", cancelErr)
			}
			cleanupAPICtx, removeErr = removeRunnerByScope(workerCtx, cleanupAPICtx, githubClient, pluginConfig, *runner.ID)
		}
		if removeErr != nil {
			logging.Error(cleanupAPICtx, "error executing githubClient.Actions.RemoveRunner", "error", removeErr)
			return
		}
		logging.Info(cleanupAPICtx, "successfully removed runner")
		break
	}
}

func removeRunnerByScope(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *github.Client,
	pluginConfig config.Plugin,
	runnerID int64,
) (context.Context, error) {
	var removeErr error
	switch pluginConfig.GitHubScope() {
	case config.GitHubScopeRepository:
		pluginCtx, _, _, removeErr = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
			resp, err := githubClient.Actions.RemoveRunner(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, runnerID)
			return resp, resp, err
		})
	case config.GitHubScopeEnterprise:
		pluginCtx, _, _, removeErr = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
			resp, err := githubClient.Enterprise.RemoveRunner(pluginCtx, pluginConfig.Enterprise, runnerID)
			return resp, resp, err
		})
	default:
		pluginCtx, _, _, removeErr = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.Response, *github.Response, error) {
			resp, err := githubClient.Actions.RemoveOrganizationRunner(pluginCtx, pluginConfig.Owner, runnerID)
			return resp, resp, err
		})
	}
	return pluginCtx, removeErr
}
