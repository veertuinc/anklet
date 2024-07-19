package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v61/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	dbFunctions "github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	webhook "github.com/veertuinc/webhooks/v6/github"
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

func exists_in_array_regex(array_to_search_in []string, desired []string) bool {
	if len(desired) == 0 || desired[0] == "" {
		return false
	}
	for _, desired_string := range desired {
		// fmt.Printf("  desired_string: %s\n", desired_string)
		found := false
		for _, item := range array_to_search_in {
			// fmt.Printf("    item: %s\n", item)
			// Check if the desired_string is a valid regex pattern
			if rege, err := regexp.Compile(desired_string); err == nil {
				// If it's a valid regex, check for a regex match
				sanitizedSplit := slices.DeleteFunc(rege.Split(item, -1), func(e string) bool {
					return e == ""
				})
				// fmt.Printf("    sanitizedSplit: %+v\n", sanitizedSplit)
				if len(sanitizedSplit) == 0 {
					// fmt.Println("      regex match")
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func does_not_exist_in_array_regex(array_to_search_in []string, excluded []string) bool {
	if len(excluded) == 0 || excluded[0] == "" {
		return true
	}
	for _, excluded_string := range excluded {
		// fmt.Printf("  excluded_string: %s\n", excluded_string)
		found := false
		for _, item := range array_to_search_in {
			// fmt.Printf("    item: %s\n", item)
			// Check if the desired_string is a valid regex pattern
			if rege, err := regexp.Compile(excluded_string); err == nil {
				// If it's a valid regex, check for a regex match
				sanitizedSplit := slices.DeleteFunc(rege.Split(item, -1), func(e string) bool {
					return e == ""
				})
				// fmt.Printf("    sanitizedSplit: %+v\n", sanitizedSplit)
				if len(sanitizedSplit) > 0 {
					// fmt.Println("      regex match")
					found = true
					break
				}
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

func setLoggingContext(serviceCtx context.Context, workflowRunJob WorkflowRunJobDetail) context.Context {
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("workflowName", workflowRunJob.WorkflowName))
	// not available in webhooks
	// serviceCtx = logging.AppendCtx(serviceCtx, slog.String("workflowRunName", workflowRunJob.WorkflowRunName))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.Int64("workflowRunId", workflowRunJob.RunID))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.Int64("workflowJobId", workflowRunJob.JobID))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("workflowJobName", workflowRunJob.JobName))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("uniqueId", workflowRunJob.UniqueID))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("ankaTemplate", workflowRunJob.AnkaTemplate))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("ankaTemplateTag", workflowRunJob.AnkaTemplateTag))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("jobURL", workflowRunJob.JobURL))
	return serviceCtx
}

func CheckForCompletedJobs(
	workerCtx context.Context,
	serviceCtx context.Context,
	logger *slog.Logger,
	completedJobChannel chan bool,
	serviceDatabaseKeyName string,
	ranOnce chan struct{},
) {
	fmt.Println("CheckForCompletedJobs")
	databaseContainer, err := dbFunctions.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database from context", "err", err)
		logging.Panic(workerCtx, serviceCtx, "error getting database from context")
	}
	defer func() {
		fmt.Println("CheckForCompletedJobs defer")
		// ensure, outside of needing to return on error, that the following always runs
		select {
		case <-ranOnce:
			// already closed, do nothing
		default:
			close(ranOnce)
		}
	}()
	for {
		// do not use 'continue' in the loop or else the ranOnce won't happen
		time.Sleep(3 * time.Second)
		fmt.Println("CheckForCompletedJobs default")
		select {
		case <-completedJobChannel:
			fmt.Println("CheckForCompletedJobs completedJobChannel at top")
			return
		case <-serviceCtx.Done():
			fmt.Println("CheckForCompletedJobs serviceCtx.Done()")
			return
		default:
		}
		// get the job ID
		var existingJob webhook.WorkflowJobPayload
		var existingJobID int64
		existingJobString, err := databaseContainer.Client.LIndex(serviceCtx, serviceDatabaseKeyName, -1).Result()
		if err != redis.Nil { // handle no job for service
			if err == nil {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(existingJobString), &payload); err != nil {
					logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
					return
				}
				payloadBytes, err := json.Marshal(payload["payload"])
				if err != nil {
					logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
					return
				}
				err = json.Unmarshal(payloadBytes, &existingJob)
				if err != nil {
					logger.ErrorContext(serviceCtx, "error unmarshalling payload to webhook.WorkflowJobPayload", "err", err)
					return
				}
				existingJobID = existingJob.WorkflowJob.ID
				// check if there is already a completed job queued for the server
				// // this can happen if the service crashes or is stopped before it finalizes cleanup
				count, err := databaseContainer.Client.LLen(serviceCtx, serviceDatabaseKeyName+"/completed").Result()
				if err != nil {
					logger.ErrorContext(serviceCtx, "error getting count of objects in "+serviceDatabaseKeyName+"/completed", "err", err)
					return
				}
				if count > 0 {
					completedJobChannel <- true
					return
				} else {
					// get count of completed jobs in the queue
					count, err = databaseContainer.Client.LLen(serviceCtx, "anklet/jobs/github/completed").Result()
					if err != nil {
						logger.ErrorContext(serviceCtx, "error getting count of objects in anklet/jobs/github/completed", "err", err)
						return
					}
					// logger.InfoContext(serviceCtx, "count of objects in anklet/jobs/github/completed", "count", count)
					completedJobs, err := databaseContainer.Client.LRange(serviceCtx, "anklet/jobs/github/completed", 0, count-1).Result()
					if err != nil {
						logger.ErrorContext(serviceCtx, "error getting list of completed jobs", "err", err)
						return
					}
					for _, completedJob := range completedJobs {
						var completedJobWebhook webhook.WorkflowJobPayload
						err := json.Unmarshal([]byte(completedJob), &completedJobWebhook)
						if err != nil {
							logger.ErrorContext(serviceCtx, "error unmarshalling completed job", "err", err)
							return
						}
						if completedJobWebhook.WorkflowJob.ID == existingJobID {
							// remove the completed job we found
							_, err = databaseContainer.Client.LRem(serviceCtx, "anklet/jobs/github/completed", 1, completedJob).Result()
							if err != nil {
								logger.ErrorContext(serviceCtx, "error removing completedJob from anklet/jobs/github/completed", "err", err)
								return
							}
							// delete the existing service task
							// _, err = databaseContainer.Client.Del(serviceCtx, serviceDatabaseKeyName).Result()
							// if err != nil {
							// 	logger.ErrorContext(serviceCtx, "error deleting all objects from "+serviceDatabaseKeyName, "err", err)
							// 	return
							// }
							// add a task for the completed job so we know the clean up
							_, err = databaseContainer.Client.LPush(serviceCtx, serviceDatabaseKeyName+"/completed", completedJob).Result()
							if err != nil {
								logger.ErrorContext(serviceCtx, "error inserting completed job into list", "err", err)
								return
							}
							completedJobChannel <- true
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
	}
}

// cleanup will pop off the last item from the list and, based on its type, perform the appropriate cleanup action
// this assumes the plugin code created a list item to represent the thing to clean up
func cleanup(workerCtx context.Context, serviceCtx context.Context, logger *slog.Logger, serviceDatabaseKeyName string, returnToQueue chan bool) {
	logger.InfoContext(serviceCtx, "cleaning up")
	// create an idependent copy of the serviceCtx so we can do cleanup even if serviceCtx got "context canceled"
	cleanupContext := context.Background()
	serviceDatabase, err := dbFunctions.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database from context", "err", err)
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("database"), serviceDatabase)
	cleanupContext, cancel := context.WithCancel(cleanupContext)
	defer cancel()
	databaseContainer, err := dbFunctions.GetDatabaseFromContext(cleanupContext)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database from context", "err", err)
		return
	}
	for {
		var jobJSON string
		fmt.Println("DO IT")
		time.Sleep(10 * time.Second)
		exists, err := databaseContainer.Client.Exists(cleanupContext, serviceDatabaseKeyName+"/cleaning").Result()
		if err != nil {
			logger.ErrorContext(cleanupContext, "error checking if cleaning up already in progress", "err", err)
			select {
			case returnToQueue <- true:
			default:
				logger.WarnContext(serviceCtx, "unable to return task to queue")
			}
		}
		if exists == 1 {
			logger.InfoContext(serviceCtx, "cleaning up already in progress; getting job")
			jobJSON, err = databaseContainer.Client.LIndex(cleanupContext, serviceDatabaseKeyName+"/cleaning", 0).Result()
			if err != nil {
				logger.ErrorContext(serviceCtx, "error getting job from the list", "err", err)
				return
			}
		} else {
			// pop the job from the list and push it to the cleaning list
			jobJSON, err = databaseContainer.Client.RPopLPush(cleanupContext, serviceDatabaseKeyName, serviceDatabaseKeyName+"/cleaning").Result()
			if err == redis.Nil {
				break
			} else if err != nil {
				logger.ErrorContext(serviceCtx, "error popping job from the list", "err", err)
				return
			}
		}
		var typedJob map[string]interface{}
		if err := json.Unmarshal([]byte(jobJSON), &typedJob); err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
			return
		}
		switch typedJob["type"] {
		case "anka.VM":
			ankaCLI := anka.GetAnkaCLIFromContext(serviceCtx)
			vm := typedJob["payload"].(anka.VM)
			ankaCLI.AnkaDelete(workerCtx, serviceCtx, &vm)
		case "WorkflowJobPayload":
			var hook webhook.WorkflowJobPayload
			var payload map[string]interface{}
			payloadJSON, err := json.Marshal(typedJob)
			if err != nil {
				logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
				return
			}
			if err := json.Unmarshal(payloadJSON, &payload); err != nil {
				logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
				return
			}
			payloadBytes, err := json.Marshal(payload["payload"])
			if err != nil {
				logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
				return
			}
			err = json.Unmarshal(payloadBytes, &hook)
			if err != nil {
				logger.ErrorContext(serviceCtx, "error unmarshalling payload to webhook.WorkflowJobPayload", "err", err)
				return
			}
			// if it was an error, just return it to the main queue for another anklet to handle
			select {
			case <-returnToQueue:
				fmt.Println("returnToQueue")
				_, err := databaseContainer.Client.RPopLPush(cleanupContext, serviceDatabaseKeyName+"/cleaning", "anklet/jobs/github/queued").Result()
				if err != nil {
					logger.ErrorContext(serviceCtx, "error pushing job to queued", "err", err)
					return
				}
			default:
			}
			logger.InfoContext(serviceCtx, "finalized cleaning of workflow job", "workflowJobID", hook.WorkflowJob.ID)
		default:
			logger.ErrorContext(serviceCtx, "unknown job type", "job", typedJob)
			return
		}
		databaseContainer.Client.Del(cleanupContext, serviceDatabaseKeyName+"/cleaning")
	}
	databaseContainer.Client.Del(cleanupContext, serviceDatabaseKeyName)
}

func Run(workerCtx context.Context, serviceCtx context.Context, serviceCancel context.CancelFunc, logger *slog.Logger) {

	service := config.GetServiceFromContext(serviceCtx)

	if service.Token == "" && service.PrivateKey == "" {
		logging.Panic(workerCtx, serviceCtx, "token and private_key are not set in ankalet.yaml:services:"+service.Name+":token/private_key")
	}
	if service.PrivateKey != "" && (service.AppID == 0 || service.InstallationID == 0) {
		logging.Panic(workerCtx, serviceCtx, "private_key, app_id, and installation_id must all be set in ankalet.yaml:services:"+service.Name+"")
	}
	if service.Owner == "" {
		logging.Panic(workerCtx, serviceCtx, "owner is not set in ankalet.yaml:services:"+service.Name+":owner")
	}
	if service.Repo == "" {
		logging.Panic(workerCtx, serviceCtx, "repo is not set in anklet.yaml:services:"+service.Name+":repo")
	}

	hostHasVmCapacity := anka.HostHasVmCapacity(serviceCtx)
	if !hostHasVmCapacity {
		logger.DebugContext(serviceCtx, "host does not have vm capacity")
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

	githubWrapperClient := internalGithub.NewGitHubClientWrapper(githubClient)
	serviceCtx = context.WithValue(serviceCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("repo", service.Repo))
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("owner", service.Owner))
	serviceDatabaseKeyName := "anklet/jobs/github/service/" + service.Name
	repositoryURL := fmt.Sprintf("https://github.com/%s/%s", service.Owner, service.Repo)
	completedJobChannel := make(chan bool, 1)
	returnToQueue := make(chan bool, 1) // if any errors, we need to return the task to the queue instead of deleting it
	// wait group so we can wait for the goroutine to finish before exiting the service
	var wg sync.WaitGroup
	wg.Add(1)

	databaseContainer, err := dbFunctions.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database from context", "err", err)
		return
	}

	defer func() {
		fmt.Println("defer")
		close(completedJobChannel)
		fmt.Println("wg.Wait()")
		wg.Wait()
		fmt.Println("wg.Wait() done")
		// cleanup after we exit the check for completed so we can clean up the environment if a completed job was received
		cleanup(workerCtx, serviceCtx, logger, serviceDatabaseKeyName, returnToQueue)
	}()

	// check constantly for a cancelled webhook to be received for our job
	ranOnce := make(chan struct{})
	go func() {
		fmt.Println("CheckForCompletedJobs goroutine")
		CheckForCompletedJobs(workerCtx, serviceCtx, logger, completedJobChannel, serviceDatabaseKeyName, ranOnce)
		wg.Done()
		fmt.Println("CheckForCompletedJobs goroutine done")
	}()
	fmt.Println("ranOnce")
	<-ranOnce // wait for the goroutine to run at least once
	fmt.Println("ranOnce done")

	select {
	case <-completedJobChannel:
		logger.InfoContext(serviceCtx, "completed job found")
		return
	case <-serviceCtx.Done():
		logger.WarnContext(serviceCtx, "context canceled before completed job found")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	default:
	}

	var queuedJob webhook.WorkflowJobPayload
	var wrappedPayload map[string]interface{}
	var wrappedPayloadJSON string
	wrappedPayloadJSON, err = databaseContainer.Client.LIndex(serviceCtx, serviceDatabaseKeyName+"test", -1).Result()
	if err != nil && err != redis.Nil {
		logger.ErrorContext(serviceCtx, "error getting last object from "+serviceDatabaseKeyName, "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	if wrappedPayloadJSON == "" {
		wrappedPayloadJSON, err = databaseContainer.Client.RPopLPush(serviceCtx, "anklet/jobs/github/queued", serviceDatabaseKeyName).Result()
		if err == redis.Nil {
			logger.DebugContext(serviceCtx, "no queued jobs found")
			return
		}
		if err != nil {
			logger.ErrorContext(serviceCtx, "error getting queued jobs", "err", err)
			return
		}
		if err := json.Unmarshal([]byte(wrappedPayloadJSON), &wrappedPayload); err != nil {
			logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
			select {
			case returnToQueue <- true:
			default:
				logger.WarnContext(serviceCtx, "unable to return task to queue")
			}
			return
		}
	}

	if err := json.Unmarshal([]byte(wrappedPayloadJSON), &wrappedPayload); err != nil {
		logger.ErrorContext(serviceCtx, "error unmarshalling job", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	payloadBytes, err := json.Marshal(wrappedPayload["payload"])
	if err != nil {
		logger.ErrorContext(serviceCtx, "error marshalling payload", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	if err := json.Unmarshal(payloadBytes, &queuedJob); err != nil {
		logger.ErrorContext(serviceCtx, "error unmarshalling payload to webhook.WorkflowJobPayload", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	serviceCtx = logging.AppendCtx(serviceCtx, slog.Int64("workflowJobID", queuedJob.WorkflowJob.ID))

	logger.DebugContext(serviceCtx, "queued job found", "queuedJob", queuedJob)

	// get the unique unique-id for this job
	// this ensures that multiple jobs in the same workflow run don't compete for the same runner
	uniqueID := extractLabelValue(queuedJob.WorkflowJob.Labels, "unique-id:")
	if uniqueID == "" {
		logger.WarnContext(serviceCtx, "unique-id label not found or empty; something wrong with your workflow yaml")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("uniqueID", uniqueID))
	ankaTemplate := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template:")
	if ankaTemplate == "" {
		logger.WarnContext(serviceCtx, "warning: unable to find Anka Template specified in labels - skipping")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("ankaTemplate", ankaTemplate))
	ankaTemplateTag := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template-tag:")
	if ankaTemplateTag == "" {
		ankaTemplateTag = "(using latest)"
	}
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("ankaTemplateTag", ankaTemplateTag))

	workflowJob := WorkflowRunJobDetail{
		JobID:           queuedJob.WorkflowJob.ID,
		JobName:         queuedJob.WorkflowJob.Name,
		JobURL:          queuedJob.WorkflowJob.HTMLURL,
		WorkflowName:    queuedJob.WorkflowJob.WorkflowName,
		AnkaTemplate:    ankaTemplate,
		AnkaTemplateTag: ankaTemplateTag,
		RunID:           queuedJob.WorkflowJob.RunID,
		UniqueID:        uniqueID,
		Labels:          queuedJob.WorkflowJob.Labels,
	}

	fmt.Println("NEW =====================================================")

	// get anka CLI
	ankaCLI := anka.GetAnkaCLIFromContext(serviceCtx)

	logger.InfoContext(serviceCtx, "handling anka workflow run job")
	metrics.UpdateService(workerCtx, serviceCtx, logger, metrics.Service{
		Status: "running",
	})

	// See if VM Template existing already
	//TODO: be able to interrupt this
	templateTagExistsError := ankaCLI.EnsureVMTemplateExists(workerCtx, serviceCtx, workflowJob.AnkaTemplate, workflowJob.AnkaTemplateTag)
	if templateTagExistsError != nil {
		logger.WarnContext(serviceCtx, "error ensuring vm template exists", "err", templateTagExistsError)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled during vm template check")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	// Get runner registration token
	serviceCtx, repoRunnerRegistration, response, err := ExecuteGitHubClientFunction[github.RegistrationToken](serviceCtx, logger, func() (*github.RegistrationToken, *github.Response, error) {
		repoRunnerRegistration, resp, err := githubClient.Actions.CreateRegistrationToken(context.Background(), service.Owner, service.Repo)
		return repoRunnerRegistration, resp, err
	})
	if err != nil {
		logger.ErrorContext(serviceCtx, "error creating registration token", "err", err, "response", response)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	if *repoRunnerRegistration.Token == "" {
		logger.ErrorContext(serviceCtx, "registration token is empty; something wrong with github or your service token", "response", response)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled before ObtainAnkaVM")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	// Obtain Anka VM (and name)
	serviceCtx, vm, err := ankaCLI.ObtainAnkaVM(workerCtx, serviceCtx, workflowJob.AnkaTemplate)
	// defer ankaCLI.AnkaDelete(workerCtx, serviceCtx, vm)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error obtaining anka vm", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	wrappedVM := map[string]interface{}{
		"type":    "anka.VM",
		"payload": vm,
	}
	wrappedVmJSON, err := json.Marshal(wrappedVM)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error marshalling vm to json", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	err = databaseContainer.Client.LPush(serviceCtx, serviceDatabaseKeyName, wrappedVmJSON).Err()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error pushing vm data to database", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	serviceCancel()

	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled after ObtainAnkaVM")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	// Install runner
	globals := config.GetGlobalsFromContext(serviceCtx)
	logger.InfoContext(serviceCtx, "installing github runner inside of vm")
	err = ankaCLI.AnkaCopy(serviceCtx,
		globals.PluginsPath+"/github/install-runner.bash",
		globals.PluginsPath+"/github/register-runner.bash",
		globals.PluginsPath+"/github/start-runner.bash",
	)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error executing anka copy", "err", err)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled before install runner")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	installRunnerErr := ankaCLI.AnkaRun(serviceCtx, "./install-runner.bash")
	if installRunnerErr != nil {
		logger.ErrorContext(serviceCtx, "error executing install-runner.bash", "err", installRunnerErr)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	// Register runner
	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled before register runner")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	registerRunnerErr := ankaCLI.AnkaRun(serviceCtx,
		"./register-runner.bash",
		vm.Name, *repoRunnerRegistration.Token, repositoryURL, strings.Join(workflowJob.Labels, ","),
	)
	if registerRunnerErr != nil {
		logger.ErrorContext(serviceCtx, "error executing register-runner.bash", "err", registerRunnerErr)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	defer removeSelfHostedRunner(serviceCtx, *vm, workflowJob.RunID)
	// Install and Start runner
	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled before start runner")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	startRunnerErr := ankaCLI.AnkaRun(serviceCtx, "./start-runner.bash")
	if startRunnerErr != nil {
		logger.ErrorContext(serviceCtx, "error executing start-runner.bash", "err", startRunnerErr)
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}
	if serviceCtx.Err() != nil {
		logger.WarnContext(serviceCtx, "context canceled before jobCompleted checks")
		select {
		case returnToQueue <- true:
		default:
			logger.WarnContext(serviceCtx, "unable to return task to queue")
		}
		return
	}

	// Watch for job completion
	jobCompleted := false
	// logCounter := 0
	for !jobCompleted {
		if serviceCtx.Err() != nil {
			logger.WarnContext(serviceCtx, "context canceled while watching for job completion")
			select {
			case returnToQueue <- true:
			default:
				logger.WarnContext(serviceCtx, "unable to return task to queue")
			}
			return
		}
		// serviceCtx, currentJob, response, err := ExecuteGitHubClientFunction[github.WorkflowJob](serviceCtx, logger, func() (*github.WorkflowJob, *github.Response, error) {
		// 	currentJob, resp, err := githubClient.Actions.GetWorkflowJobByID(context.Background(), service.Owner, service.Repo, workflowRunJob.JobID)
		// 	return currentJob, resp, err
		// })
		// if err != nil {
		// 	logger.ErrorContext(serviceCtx, "error executing githubClient.Actions.GetWorkflowJobByID", "err", err, "response", response)
		// 	return
		// }
		// if *currentJob.Status == "completed" {
		// 	jobCompleted = true
		// 	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("conclusion", *currentJob.Conclusion))
		// 	logger.InfoContext(serviceCtx, "job completed", "job_id", workflowRunJob.JobID)
		// 	if *currentJob.Conclusion == "success" {
		// 		metricsData := metrics.GetMetricsDataFromContext(workerCtx)
		// 		metricsData.IncrementTotalSuccessfulRunsSinceStart()
		// 		metricsData.UpdateService(serviceCtx, logger, metrics.Service{
		// 			Name:                    service.Name,
		// 			LastSuccessfulRun:       time.Now(),
		// 			LastSuccessfulRunJobUrl: workflowRunJob.JobURL,
		// 		})
		// 	} else if *currentJob.Conclusion == "failure" {
		// 		metricsData := metrics.GetMetricsDataFromContext(workerCtx)
		// 		metricsData.IncrementTotalFailedRunsSinceStart()
		// 		metricsData.UpdateService(serviceCtx, logger, metrics.Service{
		// 			Name:                service.Name,
		// 			LastFailedRun:       time.Now(),
		// 			LastFailedRunJobUrl: workflowRunJob.JobURL,
		// 		})
		// 	}
		// } else if logCounter%2 == 0 {
		// 	if serviceCtx.Err() != nil {
		// 		logger.WarnContext(serviceCtx, "context canceled during job status check")
		// 		return
		// 	}
		// 	logger.InfoContext(serviceCtx, "job still in progress", "job_id", workflowRunJob.JobID)
		// 	time.Sleep(5 * time.Second) // Wait before checking the job status again
		// }
		// logCounter++
	}
}

// removeSelfHostedRunner handles removing a registered runner if the registered runner was orphaned somehow
// it's extra safety should the runner not be registered with --ephemeral
func removeSelfHostedRunner(serviceCtx context.Context, vm anka.VM, workflowRunID int64) {
	logger := logging.GetLoggerFromContext(serviceCtx)
	service := config.GetServiceFromContext(serviceCtx)
	githubClient := internalGithub.GetGitHubClientFromContext(serviceCtx)
	serviceCtx, runnersList, response, err := ExecuteGitHubClientFunction[github.Runners](serviceCtx, logger, func() (*github.Runners, *github.Response, error) {
		runnersList, resp, err := githubClient.Actions.ListRunners(context.Background(), service.Owner, service.Repo, &github.ListOptions{})
		return runnersList, resp, err
	})
	if err != nil {
		logger.ErrorContext(serviceCtx, "error executing githubClient.Actions.ListRunners", "err", err, "response", response)
		return
	}
	if len(runnersList.Runners) == 0 {
		logger.DebugContext(serviceCtx, "no runners found to delete (not an error)")
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
					serviceCtx, workflowRun, _, err := ExecuteGitHubClientFunction[github.WorkflowRun](serviceCtx, logger, func() (*github.WorkflowRun, *github.Response, error) {
						workflowRun, resp, err := githubClient.Actions.GetWorkflowRunByID(context.Background(), service.Owner, service.Repo, workflowRunID)
						return workflowRun, resp, err
					})
					if err != nil {
						logger.ErrorContext(serviceCtx, "error getting workflow run by ID", "err", err)
						return
					}
					if *workflowRun.Status == "completed" || (workflowRun.Conclusion != nil && *workflowRun.Conclusion == "cancelled") {
						break
					} else {
						logger.WarnContext(serviceCtx, "workflow run is still active... waiting for cancellation so we can clean up the runner...", "workflow_run_id", workflowRunID)
						if !cancelSent { // this has to happen here so that it doesn't error with "409 Cannot cancel a workflow run that is completed. " if the job is already cancelled
							serviceCtx, cancelResponse, _, cancelErr := ExecuteGitHubClientFunction[github.Response](serviceCtx, logger, func() (*github.Response, *github.Response, error) {
								resp, err := githubClient.Actions.CancelWorkflowRunByID(context.Background(), service.Owner, service.Repo, workflowRunID)
								return resp, nil, err
							})
							// don't use cancelResponse.Response.StatusCode or else it'll error with SIGSEV
							if cancelErr != nil && !strings.Contains(cancelErr.Error(), "try again later") {
								logger.ErrorContext(serviceCtx, "error executing githubClient.Actions.CancelWorkflowRunByID", "err", cancelErr, "response", cancelResponse)
								break
							}
							cancelSent = true
						}
						time.Sleep(10 * time.Second)
					}
				}
				serviceCtx, _, _, err = ExecuteGitHubClientFunction[github.Response](serviceCtx, logger, func() (*github.Response, *github.Response, error) {
					response, err := githubClient.Actions.RemoveRunner(context.Background(), service.Owner, service.Repo, *runner.ID)
					return response, nil, err
				})
				if err != nil {
					logger.ErrorContext(serviceCtx, "error executing githubClient.Actions.RemoveRunner", "err", err)
					return
				} else {
					logger.InfoContext(serviceCtx, "successfully removed runner")
				}
				break
			}
		}
		// if !found {
		// 	logger.InfoContext(serviceCtx, "no matching runner found")
		// }
	}
}
