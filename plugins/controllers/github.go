package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/webhooks/v6/github"
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

// Start runs the HTTP server
func Run(workerCtx context.Context, serviceCtx context.Context, logger *slog.Logger) {
	service := config.GetServiceFromContext(serviceCtx)
	hook, _ := github.New(github.Options.Secret(service.Secret))
	server := &http.Server{Addr: ":" + service.Port}
	http.HandleFunc("/jobs/v1/receiver", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
		if err != nil {
			logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
			return
		}
		// loadedConfig := config.GetLoadedConfigFromContext(workerCtx)
		// logger.InfoContext(workerCtx, "loaded config", slog.Any("config", loadedConfig))
		// Handle the v1 jobs endpoint
		payload, err := hook.Parse(r, github.WorkflowJobEvent)
		if err != nil {
			if err == github.ErrEventNotFound {
				logger.ErrorContext(serviceCtx, "error parsing event", "error", err)
			}
		}
		switch workflow := payload.(type) {
		case github.WorkflowJobPayload:
			// Do whatever you want from here...
			logger.InfoContext(serviceCtx, "received workflow job", slog.Any("workflowJob", workflow))
			if workflow.Action == "queued" {
				if exists_in_array_exact(workflow.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					payloadJSON, err := json.Marshal(payload)
					if err != nil {
						logger.ErrorContext(serviceCtx, "error converting payload to JSON", "error", err)
						return
					}
					push := databaseContainer.Client.LPush(serviceCtx, "jobs/github/queued", payloadJSON)
					if push.Err() != nil {
						logger.ErrorContext(serviceCtx, "error pushing job to queue", "error", push.Err())
						return
					}
					logger.InfoContext(serviceCtx, "job pushed to queued queue", "json", string(payloadJSON))
				}
			} else if workflow.Action == "completed" {
				if exists_in_array_exact(workflow.WorkflowJob.Labels, []string{"self-hosted", "anka"}) {
					payloadJSON, err := json.Marshal(payload)
					if err != nil {
						logger.ErrorContext(serviceCtx, "error converting payload to JSON", "error", err)
						return
					}
					push := databaseContainer.Client.LPush(serviceCtx, "jobs/github/completed", payloadJSON)
					if push.Err() != nil {
						logger.ErrorContext(serviceCtx, "error pushing job to queue", "error", push.Err())
						return
					}
					logger.InfoContext(serviceCtx, "job pushed to completed queue", "json", string(payloadJSON))
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
			logger.ErrorContext(serviceCtx, "controller listener error", "error", err)
		}
	}()
	<-serviceCtx.Done()
	logger.InfoContext(serviceCtx, "shutting down controller")
	if err := server.Shutdown(context.Background()); err != nil {
		logger.ErrorContext(serviceCtx, "controller shutdown error", "error", err)
	}
}
