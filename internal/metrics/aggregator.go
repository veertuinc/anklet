package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

// Start runs the HTTP server
func (s *Server) StartAggregatorServer(workerCtx context.Context, logger *slog.Logger, soloReceiver bool) {
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
			return
		}
		loadedConfig, err := config.GetLoadedConfigFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "error getting loaded config from context", "error", err)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetrics(workerCtx, logger, databaseContainer, loadedConfig)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger, databaseContainer, loadedConfig)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte("please use /metrics/v1"))
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
			var metricsData MetricsData
			err = json.Unmarshal([]byte(value), &metricsData)
			if err != nil {
				logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
				return
			}
			soloReceiver := false
			for _, plugin := range metricsData.Plugins {
				var pluginName string
				var Name string
				var ownerName string
				var repoName string
				var status string
				var lastSuccessfulRunJobUrl string
				var lastFailedRunJobUrl string
				var lastSuccessfulRun time.Time
				var lastFailedRun time.Time
				var statusSince time.Time
				pluginMap, ok := plugin.(map[string]interface{})
				if !ok {
					logger.ErrorContext(workerCtx, "error asserting plugin to map", "plugin", plugin)
					return
				}
				pluginName = pluginMap["plugin_name"].(string)
				Name = pluginMap["name"].(string)
				ownerName = pluginMap["owner_name"].(string)
				if pluginMap["repo_name"] != nil {
					repoName = pluginMap["repo_name"].(string)
				}
				status = pluginMap["status"].(string)
				statusSince, err = time.Parse(time.RFC3339, pluginMap["status_since"].(string))
				if err != nil {
					logger.ErrorContext(workerCtx, "error parsing status since", "error", err)
					return
				}
				if !strings.Contains(pluginName, "_receiver") {
					lastSuccessfulRunJobUrl = pluginMap["last_successful_run_job_url"].(string)
					lastFailedRunJobUrl = pluginMap["last_failed_run_job_url"].(string)
					lastSuccessfulRun, err = time.Parse(time.RFC3339, pluginMap["last_successful_run"].(string))
					if err != nil {
						logger.ErrorContext(workerCtx, "error parsing last successful run", "error", err)
						return
					}
					lastFailedRun, err = time.Parse(time.RFC3339, pluginMap["last_failed_run"].(string))
					if err != nil {
						logger.ErrorContext(workerCtx, "error parsing last failed run", "error", err)
						return
					}
				}
				if repoName == "" {
					w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,owner=%s,metricsUrl=%s} %s\n", Name, ownerName, metricsURL, status)))
				} else {
					w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,owner=%s,repo=%s,metricsUrl=%s} %s\n", Name, ownerName, repoName, metricsURL, status)))
				}
				if !strings.Contains(pluginName, "_receiver") {
					if repoName == "" {
						w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,owner=%s,job_url=%s,metricsUrl=%s} %s\n", Name, ownerName, lastSuccessfulRunJobUrl, metricsURL, lastSuccessfulRun.Format(time.RFC3339))))
						w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,owner=%s,job_url=%s,metricsUrl=%s} %s\n", Name, ownerName, lastFailedRunJobUrl, metricsURL, lastFailedRun.Format(time.RFC3339))))
					} else {
						w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,owner=%s,repo=%s,job_url=%s,metricsUrl=%s} %s\n", Name, ownerName, repoName, lastSuccessfulRunJobUrl, metricsURL, lastSuccessfulRun.Format(time.RFC3339))))
						w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,owner=%s,repo=%s,job_url=%s,metricsUrl=%s} %s\n", Name, ownerName, repoName, lastFailedRunJobUrl, metricsURL, lastFailedRun.Format(time.RFC3339))))
					}
				}
				if repoName == "" {
					w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,owner=%s,metricsUrl=%s} %s\n", Name, ownerName, metricsURL, statusSince.Format(time.RFC3339))))
				} else {
					w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,owner=%s,repo=%s,metricsUrl=%s} %s\n", Name, ownerName, repoName, metricsURL, statusSince.Format(time.RFC3339))))
				}
				if strings.Contains(pluginName, "_receiver") {
					soloReceiver = true
				} else {
					soloReceiver = false
				}
			}
			if !soloReceiver {
				w.Write([]byte(fmt.Sprintf("total_running_vms{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalRunningVMs)))
				w.Write([]byte(fmt.Sprintf("total_successful_runs_since_start{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalSuccessfulRunsSinceStart)))
				w.Write([]byte(fmt.Sprintf("total_failed_runs_since_start{metricsUrl=%s} %d\n", metricsURL, metricsData.TotalFailedRunsSinceStart)))
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

func UpdatemetricsURLDBEntry(pluginCtx context.Context, logger *slog.Logger, metricsURL string) {
	resp, err := http.Get(metricsURL + "?format=json")
	if err != nil {
		logger.ErrorContext(pluginCtx, "error fetching metrics from url", "metrics_url", metricsURL, "error", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error reading response body", "metrics_url", metricsURL, "error", err)
		return
	}
	var metricsData MetricsData
	err = json.Unmarshal(body, &metricsData)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error unmarshalling metrics data", "metrics_url", metricsURL, "error", err)
		return
	}
	logger.DebugContext(pluginCtx, "obtained metrics from url", "metrics", metricsData)
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting database client from context", "error", err)
		return
	}
	// Store the JSON data in Redis
	setting := databaseContainer.Client.Set(pluginCtx, metricsURL, body, 0)
	if setting.Err() != nil {
		logger.ErrorContext(pluginCtx, "error storing metrics data in Redis", "error", setting.Err())
		return
	}
	exists, err := databaseContainer.Client.Exists(pluginCtx, metricsURL).Result()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error checking if key exists in Redis", "key", metricsURL, "error", err)
		return
	}
	logger.DebugContext(pluginCtx, "successfully stored metrics data in Redis", "key", metricsURL, "exists", exists)
}
