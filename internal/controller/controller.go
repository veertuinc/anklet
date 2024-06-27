package controller

import (
	"context"
	"log/slog"
	"net/http"
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

// Start runs the HTTP server
func (s *Server) Start(workerCtx context.Context, logger *slog.Logger) {
	http.HandleFunc("/jobs/v1", func(w http.ResponseWriter, r *http.Request) {
		// databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		// if err != nil {
		// 	logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
		// 	return
		// }
		// loadedConfig := config.GetLoadedConfigFromContext(workerCtx)
		// logger.InfoContext(workerCtx, "loaded config", slog.Any("config", loadedConfig))
		// Handle the v1 jobs endpoint
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("v1 jobs endpoint"))
	})
	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte("please use /jobs/v1"))
	})
	http.ListenAndServe(":"+s.Port, nil)
}
