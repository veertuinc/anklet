package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

// Start runs the HTTP server
func (s *Server) StartAggregatorServer(workerCtx context.Context, logger *slog.Logger) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
			return
		}
		loadedConfig := config.GetLoadedConfigFromContext(workerCtx)
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetrics(workerCtx, logger, databaseContainer, loadedConfig)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger, databaseContainer, loadedConfig)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.ListenAndServe(":"+s.Port, nil)
}

func (s *Server) handleAggregatorJsonMetrics(workerCtx context.Context, logger *slog.Logger, databaseContainer *database.Database, loadedConfig *config.Config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		combinedMetrics := make(map[string]MetricsData)
		for _, metricsURL := range loadedConfig.Metrics.MetricsURLs {
			value, err := databaseContainer.Client.Get(workerCtx, metricsURL).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "error getting value from Redis", "key", metricsURL, "error", err)
				return
			}
			var metricsData MetricsData
			err = json.Unmarshal([]byte(value), &metricsData)
			if err != nil {
				logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
				return
			}
			combinedMetrics[metricsURL] = metricsData
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(combinedMetrics)
	}
}

func (s *Server) handleAggregatorPrometheusMetrics(workerCtx context.Context, logger *slog.Logger, databaseContainer *database.Database, loadedConfig *config.Config) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		for _, metricsURL := range loadedConfig.Metrics.MetricsURLs {
			value, err := databaseContainer.Client.Get(workerCtx, metricsURL).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "error getting value from Redis", "key", metricsURL, "error", err)
				return
			}
			var metricsData MetricsDataLock
			err = json.Unmarshal([]byte(value), &metricsData)
			if err != nil {
				logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
				return
			}
			w.Write([]byte(fmt.Sprintf("total_running_vms{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalRunningVMs)))
			w.Write([]byte(fmt.Sprintf("total_successful_runs_since_start{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalSuccessfulRunsSinceStart)))
			w.Write([]byte(fmt.Sprintf("total_failed_runs_since_start{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalFailedRunsSinceStart)))
			for _, service := range metricsData.Services {
				w.Write([]byte(fmt.Sprintf("service_status{service_name=%s,plugin=%s,owner=%s,repo=%s,metricsUrl=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, metricsURL, service.Status)))
				w.Write([]byte(fmt.Sprintf("service_last_successful_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s,metricsUrl=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, service.LastSuccessfulRunJobUrl, metricsURL, service.LastSuccessfulRun.Format(time.RFC3339))))
				w.Write([]byte(fmt.Sprintf("service_last_failed_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s,metricsUrl=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, service.LastFailedRunJobUrl, metricsURL, service.LastFailedRun.Format(time.RFC3339))))
				w.Write([]byte(fmt.Sprintf("service_status_running_since{service_name=%s,plugin=%s,owner=%s,repo=%s,metricsUrl=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, metricsURL, service.StatusRunningSince.Format(time.RFC3339))))
			}
			w.Write([]byte(fmt.Sprintf("host_cpu_count{metricsUrl=%s} %d\n", metricsURL, metricsData.HostCPUCount)))
			w.Write([]byte(fmt.Sprintf("host_cpu_used_count{metricsUrl=%s} %d\n", metricsURL, metricsData.HostCPUUsedCount)))
			w.Write([]byte(fmt.Sprintf("host_cpu_usage_percentage{metricsUrl=%s} %f\n", metricsURL, metricsData.HostCPUUsagePercentage)))
			w.Write([]byte(fmt.Sprintf("host_memory_total_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostMemoryTotalBytes)))
			w.Write([]byte(fmt.Sprintf("host_memory_used_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostMemoryUsedBytes)))
			w.Write([]byte(fmt.Sprintf("host_memory_available_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostMemoryAvailableBytes)))
			w.Write([]byte(fmt.Sprintf("host_memory_usage_percentage{metricsUrl=%s} %f\n", metricsURL, metricsData.HostMemoryUsagePercentage)))
			w.Write([]byte(fmt.Sprintf("host_disk_total_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostDiskTotalBytes)))
			w.Write([]byte(fmt.Sprintf("host_disk_used_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostDiskUsedBytes)))
			w.Write([]byte(fmt.Sprintf("host_disk_available_bytes{metricsUrl=%s} %d\n", metricsURL, metricsData.HostDiskAvailableBytes)))
			w.Write([]byte(fmt.Sprintf("host_disk_usage_percentage{metricsUrl=%s} %f\n", metricsURL, metricsData.HostDiskUsagePercentage)))
		}
	}
}

func UpdatemetricsURLDBEntry(serviceCtx context.Context, logger *slog.Logger, metricsURL string) {
	resp, err := http.Get(metricsURL + "?format=json")
	if err != nil {
		logger.ErrorContext(serviceCtx, "error fetching metrics from url", "metrics_url", metricsURL, "error", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error reading response body", "metrics_url", metricsURL, "error", err)
		return
	}
	var metricsData MetricsData
	err = json.Unmarshal(body, &metricsData)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error unmarshalling metrics data", "metrics_url", metricsURL, "error", err)
		return
	}
	logger.DebugContext(serviceCtx, "obtained metrics from url", "metrics", metricsData)
	databaseContainer, err := database.GetDatabaseFromContext(serviceCtx)
	if err != nil {
		logger.ErrorContext(serviceCtx, "error getting database client from context", "error", err)
		return
	}
	// Store the JSON data in Redis
	setting := databaseContainer.Client.Set(serviceCtx, metricsURL, body, 0)
	if setting.Err() != nil {
		logger.ErrorContext(serviceCtx, "error storing metrics data in Redis", "error", setting.Err())
		return
	}
	exists, err := databaseContainer.Client.Exists(serviceCtx, metricsURL).Result()
	if err != nil {
		logger.ErrorContext(serviceCtx, "error checking if key exists in Redis", "key", metricsURL, "error", err)
		return
	}
	logger.DebugContext(serviceCtx, "successfully stored metrics data in Redis", "key", metricsURL, "exists", exists)
}
