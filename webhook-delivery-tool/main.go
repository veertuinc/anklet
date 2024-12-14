package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"github.com/google/go-github/v66/github"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
)

func main() {

	logger := logging.New()

	var err error
	var owner string
	var repo string
	var hookID int64
	var privateKey string
	var token string
	var appID int64
	var installationID int64
	var redeliverHours int
	flag.StringVar(&owner, "owner", "", "GitHub owner")
	flag.StringVar(&repo, "repo", "", "GitHub repository")
	flag.Int64Var(&hookID, "hook-id", 0, "GitHub hook ID")
	flag.StringVar(&privateKey, "private-key", "", "Path to GitHub private key")
	flag.StringVar(&token, "token", "", "GitHub token")
	flag.Int64Var(&appID, "app-id", 0, "GitHub app ID")
	flag.Int64Var(&installationID, "installation-id", 0, "GitHub installation ID")
	flag.IntVar(&redeliverHours, "redeliver-hours", 24, "Number of hours to search for redeliveries")
	flag.Parse()

	limitForHooks := time.Now().Add(-time.Hour * time.Duration(redeliverHours)) // the time we want the stop search for redeliveries

	if hookID == 0 {
		fmt.Println("hook-id is required")
		flag.PrintDefaults()
		return
	}
	if owner == "" {
		fmt.Println("owner is required")
		flag.PrintDefaults()
		return
	}

	if privateKey == "" && token == "" {
		fmt.Println("private-key (path) or token is required")
		flag.PrintDefaults()
		return
	}
	if privateKey != "" && (appID == 0 || installationID == 0) {
		fmt.Println("private_key, app-id, and installation-id must all be set")
		flag.PrintDefaults()
		return
	}

	pluginCtx := context.Background()
	workerCtx := context.Background()
	var githubClient *github.Client

	githubClient, err = internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		logger,
		privateKey,
		appID,
		installationID,
		token,
	)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error authenticating github client", "err", err)
		return
	}

	isRepoSet := repo != ""

	var hookDeliveries *[]*github.HookDelivery
	opts := &github.ListCursorOptions{PerPage: 10}
	if isRepoSet {
		pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
			hookDeliveries, response, err := githubClient.Repositories.ListHookDeliveries(pluginCtx, owner, repo, hookID, opts)
			if err != nil {
				return nil, nil, err
			}
			return &hookDeliveries, response, nil
		})
	} else {
		pluginCtx, hookDeliveries, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*[]*github.HookDelivery, *github.Response, error) {
			hookDeliveries, response, err := githubClient.Organizations.ListHookDeliveries(pluginCtx, owner, hookID, opts)
			if err != nil {
				return nil, nil, err
			}
			return &hookDeliveries, response, nil
		})
	}
	if err != nil {
		logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
		return
	}

	var toRedeliver []*github.HookDelivery
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
				// fmt.Printf("%+v\n", hookDelivery)
			}
		}
	}

	fmt.Println("Redeliveries\n======================")
	for i := len(toRedeliver) - 1; i >= 0; i-- { // make sure we process/redeliver queued before completed
		hookDelivery := toRedeliver[i]
		var gottenHookDelivery *github.HookDelivery
		var err error
		if isRepoSet {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Repositories.GetHookDelivery(pluginCtx, owner, repo, hookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		} else {
			pluginCtx, gottenHookDelivery, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.HookDelivery, *github.Response, error) {
				gottenHookDelivery, response, err := githubClient.Organizations.GetHookDelivery(pluginCtx, owner, hookID, *hookDelivery.ID)
				if err != nil {
					return nil, nil, err
				}
				return gottenHookDelivery, response, nil
			})
		}
		if err != nil {
			logger.ErrorContext(pluginCtx, "error listing hooks", "err", err)
			return
		}
		var workflowJobEvent github.WorkflowJobEvent
		err = json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEvent)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling hook request raw payload to HookResponse", "err", err)
			return
		}

		workflowJobRepo := *workflowJobEvent.Repo.Name
		var currentWorkflowJob *github.WorkflowJob
		pluginCtx, currentWorkflowJob, _, err = internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.WorkflowJob, *github.Response, error) {
			currentWorkflowJob, response, err := githubClient.Actions.GetWorkflowJobByID(pluginCtx, owner, workflowJobRepo, *workflowJobEvent.WorkflowJob.ID)
			if err != nil {
				return nil, nil, err
			}
			return currentWorkflowJob, response, nil
		})
		if err != nil {
			logger.ErrorContext(pluginCtx, "error getting workflow job", "err", err)
			return
		}

		if currentWorkflowJob.Status != nil && *currentWorkflowJob.Status != "completed" {
			fmt.Println(hookDelivery)
			continue
		}
	}
}
