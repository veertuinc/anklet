package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

// Start runs the HTTP server
func (s *Server) StartAggregatorServer(workerCtx context.Context, logger *slog.Logger) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetrics(workerCtx, logger)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.ListenAndServe(":"+s.Port, nil)
}

func (s *Server) handleAggregatorJsonMetrics(workerCtx context.Context, logger *slog.Logger) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "Error getting database client from context", "error", err)
			return
		}
		loadedConfig := config.GetLoadedConfigFromContext(workerCtx)
		var combinedMetrics []map[string]MetricsData
		for _, endpoint := range loadedConfig.Metrics.Endpoints {
			value, err := databaseContainer.Client.Get(workerCtx, endpoint).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "Error getting value from Redis", "key", endpoint, "error", err)
				return
			}
			var metricsData MetricsData
			err = json.Unmarshal([]byte(value), &metricsData)
			if err != nil {
				logger.ErrorContext(workerCtx, "Error unmarshalling metrics data", "error", err)
				return
			}
			combinedMetrics = append(combinedMetrics, map[string]MetricsData{endpoint: metricsData})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(combinedMetrics)
	}
}

func (s *Server) handleAggregatorPrometheusMetrics(parentCtx context.Context, logger *slog.Logger) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := GetMetricsDataFromContext(parentCtx)
		fmt.Fprintf(w, "%+v", metricsData)

	}
}

func UpdateEndpointMetrics(serviceCtx context.Context, logger *slog.Logger, endpoint string) {
	resp, err := http.Get(endpoint + "/metrics?format=json")
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error fetching metrics from endpoint", "endpoint", endpoint, "error", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error reading response body", "endpoint", endpoint, "error", err)
		return
	}
	var metricsData MetricsData
	err = json.Unmarshal(body, &metricsData)
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error unmarshalling metrics data", "endpoint", endpoint, "error", err)
		return
	}
	logger.InfoContext(serviceCtx, "obtained metrics from endpoint", "metrics", metricsData)
	databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting database client from context", "error", err)
		return
	}
	// Store the JSON data in Redis
	setting := databaseContainer.Client.Set(serviceCtx, endpoint, body, 0)
	if setting.Err() != nil {
		logger.ErrorContext(serviceCtx, "Error storing metrics data in Redis", "error", setting.Err())
		return
	}
	exists, err := databaseContainer.Client.Exists(serviceCtx, endpoint).Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error checking if key exists in Redis", "key", endpoint, "error", err)
		return
	}
	logger.InfoContext(serviceCtx, "Successfully stored metrics data in Redis", "key", endpoint, "exists", exists)
}
