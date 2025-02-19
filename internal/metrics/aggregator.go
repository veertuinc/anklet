package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

// Start runs the HTTP server
func (s *Server) StartAggregatorServer(
	workerCtx context.Context,
	logger *slog.Logger,
	soloReceiver bool,
) {
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetricsV1(workerCtx, logger, databaseContainer)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger, databaseContainer)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.HandleFunc("/metrics/v2", func(w http.ResponseWriter, r *http.Request) {
		databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
		if err != nil {
			logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
			return
		}
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetricsV2(workerCtx, logger, databaseContainer)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger, databaseContainer)(w, r)
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

type jsonMetricsResponse struct {
	RepoName                            string  `json:"repo_name"`
	PluginName                          string  `json:"plugin_name"`
	CustomName                          string  `json:"custom_name"`
	OwnerName                           string  `json:"owner_name"`
	LastUpdate                          string  `json:"last_update"`
	PluginStatus                        string  `json:"plugin_status"`
	PluginLastSuccessfulRun             string  `json:"plugin_last_successful_run"`
	PluginLastFailedRun                 string  `json:"plugin_last_failed_run"`
	PluginLastCanceledRun               string  `json:"plugin_last_canceled_run"`
	PluginStatusSince                   string  `json:"plugin_status_since"`
	PluginTotalRanVMs                   int     `json:"plugin_total_ran_vms"`
	PluginTotalSuccessfulRunsSinceStart int     `json:"plugin_total_successful_runs_since_start"`
	PluginTotalFailedRunsSinceStart     int     `json:"plugin_total_failed_runs_since_start"`
	PluginTotalCanceledRunsSinceStart   int     `json:"plugin_total_canceled_runs_since_start"`
	PluginLastSuccessfulRunJobUrl       string  `json:"plugin_last_successful_run_job_url"`
	PluginLastFailedRunJobUrl           string  `json:"plugin_last_failed_run_job_url"`
	PluginLastCanceledRunJobUrl         string  `json:"plugin_last_canceled_run_job_url"`
	HostCPUCount                        int     `json:"host_cpu_count"`
	HostCPUUsedCount                    int     `json:"host_cpu_used_count"`
	HostCPUUsagePercentage              float64 `json:"host_cpu_usage_percentage"`
	HostMemoryTotalBytes                int64   `json:"host_memory_total_bytes"`
	HostMemoryUsedBytes                 int64   `json:"host_memory_used_bytes"`
	HostMemoryAvailableBytes            int64   `json:"host_memory_available_bytes"`
	HostMemoryUsagePercentage           float64 `json:"host_memory_usage_percentage"`
	HostDiskTotalBytes                  int64   `json:"host_disk_total_bytes"`
	HostDiskUsedBytes                   int64   `json:"host_disk_used_bytes"`
	HostDiskAvailableBytes              int64   `json:"host_disk_available_bytes"`
	HostDiskUsagePercentage             float64 `json:"host_disk_usage_percentage"`
}

func (s *Server) handleAggregatorJsonMetricsV1(
	workerCtx context.Context,
	logger *slog.Logger,
	databaseContainer *database.Database,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		combinedMetrics := make(map[string]jsonMetricsResponse)
		w.Header().Set("Content-Type", "application/json")
		match := "anklet/metrics/*"
		var cursor uint64
		for {
			databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
			if err != nil {
				logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
				break
			}
			keys, nextCursor, err := databaseContainer.Client.Scan(workerCtx, cursor, match, 10).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "error scanning database for metrics", "error", err)
				break
			}
			for _, key := range keys {
				value, err := databaseContainer.Client.Get(workerCtx, key).Result()
				if err != nil {
					logger.ErrorContext(workerCtx, "error getting value from Redis", "key", key, "error", err)
					return
				}
				var metricsData MetricsData
				err = json.Unmarshal([]byte(value), &metricsData)
				if err != nil {
					logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
					return
				}
				for _, plugin := range metricsData.Plugins {
					var pluginName string
					var customName string
					var ownerName string
					var repoName string
					var status string
					var lastSuccessfulRunJobUrl string
					var lastFailedRunJobUrl string
					var lastCanceledRunJobUrl string
					var lastSuccessfulRun time.Time
					var lastFailedRun time.Time
					var lastCanceledRun time.Time
					var statusSince time.Time
					var totalRanVMs int
					var totalSuccessfulRunsSinceStart int
					var totalFailedRunsSinceStart int
					var totalCanceledRunsSinceStart int
					pluginMap, ok := plugin.(map[string]interface{})
					if !ok {
						logger.ErrorContext(workerCtx, "error asserting plugin to map", "plugin", plugin)
						return
					}
					pluginName = pluginMap["PluginName"].(string)
					customName = pluginMap["Name"].(string)
					ownerName = pluginMap["OwnerName"].(string)
					if pluginMap["RepoName"] != nil {
						repoName = pluginMap["RepoName"].(string)
					}
					status = pluginMap["Status"].(string)
					statusSince, err = time.Parse(time.RFC3339, pluginMap["StatusSince"].(string))
					if err != nil {
						logger.ErrorContext(workerCtx, "error parsing status since", "error", err)
						return
					}
					if !strings.Contains(pluginName, "_receiver") {
						lastSuccessfulRunJobUrl = pluginMap["LastSuccessfulRunJobUrl"].(string)
						lastFailedRunJobUrl = pluginMap["LastFailedRunJobUrl"].(string)
						lastSuccessfulRun, err = time.Parse(time.RFC3339, pluginMap["LastSuccessfulRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last successful run", "error", err)
							return
						}
						lastFailedRun, err = time.Parse(time.RFC3339, pluginMap["LastFailedRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last failed run", "error", err)
							return
						}
						lastCanceledRunJobUrl = pluginMap["LastCanceledRunJobUrl"].(string)
						lastCanceledRun, err = time.Parse(time.RFC3339, pluginMap["LastCanceledRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last canceled run", "error", err)
							return
						}
						totalRanVMs = int(pluginMap["TotalRanVMs"].(float64))
						totalSuccessfulRunsSinceStart = int(pluginMap["TotalSuccessfulRunsSinceStart"].(float64))
						totalFailedRunsSinceStart = int(pluginMap["TotalFailedRunsSinceStart"].(float64))
						totalCanceledRunsSinceStart = int(pluginMap["TotalCanceledRunsSinceStart"].(float64))
					}

					metrics := combinedMetrics[customName]

					metrics.RepoName = repoName
					metrics.PluginName = pluginName
					metrics.CustomName = customName
					metrics.OwnerName = ownerName
					metrics.LastUpdate = metricsData.LastUpdate.Format(time.RFC3339)
					metrics.PluginStatus = status
					metrics.PluginLastSuccessfulRun = lastSuccessfulRun.Format(time.RFC3339)
					metrics.PluginLastFailedRun = lastFailedRun.Format(time.RFC3339)
					metrics.PluginLastCanceledRun = lastCanceledRun.Format(time.RFC3339)
					metrics.PluginStatusSince = statusSince.Format(time.RFC3339)
					metrics.PluginTotalRanVMs = totalRanVMs
					metrics.PluginTotalSuccessfulRunsSinceStart = totalSuccessfulRunsSinceStart
					metrics.PluginTotalFailedRunsSinceStart = totalFailedRunsSinceStart
					metrics.PluginTotalCanceledRunsSinceStart = totalCanceledRunsSinceStart
					metrics.HostCPUCount = metricsData.HostCPUCount
					metrics.HostCPUUsedCount = metricsData.HostCPUUsedCount
					metrics.HostCPUUsagePercentage = metricsData.HostCPUUsagePercentage
					metrics.HostMemoryTotalBytes = int64(metricsData.HostMemoryTotalBytes)
					metrics.HostMemoryUsedBytes = int64(metricsData.HostMemoryUsedBytes)
					metrics.HostMemoryAvailableBytes = int64(metricsData.HostMemoryAvailableBytes)
					metrics.HostMemoryUsagePercentage = metricsData.HostMemoryUsagePercentage
					metrics.HostDiskTotalBytes = int64(metricsData.HostDiskTotalBytes)
					metrics.HostDiskUsedBytes = int64(metricsData.HostDiskUsedBytes)
					metrics.HostDiskAvailableBytes = int64(metricsData.HostDiskAvailableBytes)
					metrics.HostDiskUsagePercentage = metricsData.HostDiskUsagePercentage
					metrics.PluginLastSuccessfulRunJobUrl = lastSuccessfulRunJobUrl
					metrics.PluginLastFailedRunJobUrl = lastFailedRunJobUrl
					metrics.PluginLastCanceledRunJobUrl = lastCanceledRunJobUrl
					metrics.PluginStatusSince = statusSince.Format(time.RFC3339)

					if !strings.Contains(pluginName, "_receiver") {
						metrics.PluginLastSuccessfulRun = lastSuccessfulRun.Format(time.RFC3339)
						metrics.PluginLastFailedRun = lastFailedRun.Format(time.RFC3339)
						metrics.PluginLastCanceledRun = lastCanceledRun.Format(time.RFC3339)
						metrics.PluginTotalRanVMs = totalRanVMs
						metrics.PluginTotalSuccessfulRunsSinceStart = totalSuccessfulRunsSinceStart
						metrics.PluginTotalFailedRunsSinceStart = totalFailedRunsSinceStart
						metrics.PluginTotalCanceledRunsSinceStart = totalCanceledRunsSinceStart
					}

					combinedMetrics[customName] = metrics
				}
			}
			if nextCursor == 0 {
				break
			}
			cursor = nextCursor
		}
		json.NewEncoder(w).Encode(combinedMetrics)
	}
}

func (s *Server) handleAggregatorJsonMetricsV2(
	workerCtx context.Context,
	logger *slog.Logger,
	databaseContainer *database.Database,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		combinedMetrics := []jsonMetricsResponse{}
		w.Header().Set("Content-Type", "application/json")
		match := "anklet/metrics/*"
		var cursor uint64
		for {
			databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
			if err != nil {
				logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
				break
			}
			keys, nextCursor, err := databaseContainer.Client.Scan(workerCtx, cursor, match, 10).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "error scanning database for metrics", "error", err)
				break
			}
			for _, key := range keys {
				value, err := databaseContainer.Client.Get(workerCtx, key).Result()
				if err != nil {
					logger.ErrorContext(workerCtx, "error getting value from Redis", "key", key, "error", err)
					return
				}
				var metricsData MetricsData
				err = json.Unmarshal([]byte(value), &metricsData)
				if err != nil {
					logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
					return
				}
				for _, plugin := range metricsData.Plugins {
					var pluginName string
					var customName string
					var ownerName string
					var repoName string
					var status string
					var lastSuccessfulRunJobUrl string
					var lastFailedRunJobUrl string
					var lastCanceledRunJobUrl string
					var lastSuccessfulRun time.Time
					var lastFailedRun time.Time
					var lastCanceledRun time.Time
					var statusSince time.Time
					var totalRanVMs int
					var totalSuccessfulRunsSinceStart int
					var totalFailedRunsSinceStart int
					var totalCanceledRunsSinceStart int
					pluginMap, ok := plugin.(map[string]interface{})
					if !ok {
						logger.ErrorContext(workerCtx, "error asserting plugin to map", "plugin", plugin)
						return
					}
					pluginName = pluginMap["PluginName"].(string)
					customName = pluginMap["Name"].(string)
					ownerName = pluginMap["OwnerName"].(string)
					if pluginMap["RepoName"] != nil {
						repoName = pluginMap["RepoName"].(string)
					}
					status = pluginMap["Status"].(string)
					statusSince, err = time.Parse(time.RFC3339, pluginMap["StatusSince"].(string))
					if err != nil {
						logger.ErrorContext(workerCtx, "error parsing status since", "error", err)
						return
					}
					if !strings.Contains(pluginName, "_receiver") {
						lastSuccessfulRunJobUrl = pluginMap["LastSuccessfulRunJobUrl"].(string)
						lastFailedRunJobUrl = pluginMap["LastFailedRunJobUrl"].(string)
						lastSuccessfulRun, err = time.Parse(time.RFC3339, pluginMap["LastSuccessfulRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last successful run", "error", err)
							return
						}
						lastFailedRun, err = time.Parse(time.RFC3339, pluginMap["LastFailedRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last failed run", "error", err)
							return
						}
						lastCanceledRunJobUrl = pluginMap["LastCanceledRunJobUrl"].(string)
						lastCanceledRun, err = time.Parse(time.RFC3339, pluginMap["LastCanceledRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last canceled run", "error", err)
							return
						}
						totalRanVMs = int(pluginMap["TotalRanVMs"].(float64))
						totalSuccessfulRunsSinceStart = int(pluginMap["TotalSuccessfulRunsSinceStart"].(float64))
						totalFailedRunsSinceStart = int(pluginMap["TotalFailedRunsSinceStart"].(float64))
						totalCanceledRunsSinceStart = int(pluginMap["TotalCanceledRunsSinceStart"].(float64))
					}

					metrics := jsonMetricsResponse{}
					metrics.RepoName = repoName
					metrics.PluginName = pluginName
					metrics.CustomName = customName
					metrics.OwnerName = ownerName
					metrics.LastUpdate = metricsData.LastUpdate.Format(time.RFC3339)
					metrics.PluginStatus = status
					metrics.PluginLastSuccessfulRun = lastSuccessfulRun.Format(time.RFC3339)
					metrics.PluginLastFailedRun = lastFailedRun.Format(time.RFC3339)
					metrics.PluginLastCanceledRun = lastCanceledRun.Format(time.RFC3339)
					metrics.PluginStatusSince = statusSince.Format(time.RFC3339)
					metrics.PluginTotalRanVMs = totalRanVMs
					metrics.PluginTotalSuccessfulRunsSinceStart = totalSuccessfulRunsSinceStart
					metrics.PluginTotalFailedRunsSinceStart = totalFailedRunsSinceStart
					metrics.PluginTotalCanceledRunsSinceStart = totalCanceledRunsSinceStart
					metrics.HostCPUCount = metricsData.HostCPUCount
					metrics.HostCPUUsedCount = metricsData.HostCPUUsedCount
					metrics.HostCPUUsagePercentage = metricsData.HostCPUUsagePercentage
					metrics.HostMemoryTotalBytes = int64(metricsData.HostMemoryTotalBytes)
					metrics.HostMemoryUsedBytes = int64(metricsData.HostMemoryUsedBytes)
					metrics.HostMemoryAvailableBytes = int64(metricsData.HostMemoryAvailableBytes)
					metrics.HostMemoryUsagePercentage = metricsData.HostMemoryUsagePercentage
					metrics.HostDiskTotalBytes = int64(metricsData.HostDiskTotalBytes)
					metrics.HostDiskUsedBytes = int64(metricsData.HostDiskUsedBytes)
					metrics.HostDiskAvailableBytes = int64(metricsData.HostDiskAvailableBytes)
					metrics.HostDiskUsagePercentage = metricsData.HostDiskUsagePercentage
					metrics.PluginLastSuccessfulRunJobUrl = lastSuccessfulRunJobUrl
					metrics.PluginLastFailedRunJobUrl = lastFailedRunJobUrl
					metrics.PluginLastCanceledRunJobUrl = lastCanceledRunJobUrl
					metrics.PluginStatusSince = statusSince.Format(time.RFC3339)

					if !strings.Contains(pluginName, "_receiver") {
						metrics.PluginLastSuccessfulRun = lastSuccessfulRun.Format(time.RFC3339)
						metrics.PluginLastFailedRun = lastFailedRun.Format(time.RFC3339)
						metrics.PluginLastCanceledRun = lastCanceledRun.Format(time.RFC3339)
						metrics.PluginTotalRanVMs = totalRanVMs
						metrics.PluginTotalSuccessfulRunsSinceStart = totalSuccessfulRunsSinceStart
						metrics.PluginTotalFailedRunsSinceStart = totalFailedRunsSinceStart
						metrics.PluginTotalCanceledRunsSinceStart = totalCanceledRunsSinceStart
					}

					combinedMetrics = append(combinedMetrics, metrics)
				}
			}
			if nextCursor == 0 {
				break
			}
			cursor = nextCursor
		}
		json.NewEncoder(w).Encode(combinedMetrics)
	}
}

func (s *Server) handleAggregatorPrometheusMetrics(
	workerCtx context.Context,
	logger *slog.Logger,
	databaseContainer *database.Database,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		match := "anklet/metrics/*"
		var cursor uint64
		for {
			databaseContainer, err := database.GetDatabaseFromContext(workerCtx)
			if err != nil {
				logger.ErrorContext(workerCtx, "error getting database client from context", "error", err)
				break
			}
			keys, nextCursor, err := databaseContainer.Client.Scan(workerCtx, cursor, match, 10).Result()
			if err != nil {
				logger.ErrorContext(workerCtx, "error scanning database for metrics", "error", err)
				break
			}
			for _, key := range keys {
				value, err := databaseContainer.Client.Get(workerCtx, key).Result()
				if err != nil {
					logger.ErrorContext(workerCtx, "error getting value from Redis", "key", key, "error", err)
					return
				}
				var metricsData MetricsData
				err = json.Unmarshal([]byte(value), &metricsData)
				if err != nil {
					logger.ErrorContext(workerCtx, "error unmarshalling metrics data", "error", err)
					return
				}
				for _, plugin := range metricsData.Plugins {
					soloReceiver := false
					var pluginName string
					var Name string
					var ownerName string
					var repoName string
					var status string
					var lastSuccessfulRunJobUrl string
					var lastFailedRunJobUrl string
					var lastCanceledRunJobUrl string
					var lastSuccessfulRun time.Time
					var lastFailedRun time.Time
					var lastCanceledRun time.Time
					var statusSince time.Time
					var totalRanVMs int
					var totalSuccessfulRunsSinceStart int
					var totalFailedRunsSinceStart int
					var totalCanceledRunsSinceStart int
					pluginMap, ok := plugin.(map[string]interface{})
					if !ok {
						logger.ErrorContext(workerCtx, "error asserting plugin to map", "plugin", plugin)
						return
					}
					pluginName = pluginMap["PluginName"].(string)
					Name = pluginMap["Name"].(string)
					ownerName = pluginMap["OwnerName"].(string)
					if pluginMap["RepoName"] != nil {
						repoName = pluginMap["RepoName"].(string)
					}
					status = pluginMap["Status"].(string)
					statusSince, err = time.Parse(time.RFC3339, pluginMap["StatusSince"].(string))
					if err != nil {
						logger.ErrorContext(workerCtx, "error parsing status since", "error", err)
						return
					}
					if !strings.Contains(pluginName, "_receiver") {
						lastSuccessfulRunJobUrl = pluginMap["LastSuccessfulRunJobUrl"].(string)
						lastFailedRunJobUrl = pluginMap["LastFailedRunJobUrl"].(string)
						lastSuccessfulRun, err = time.Parse(time.RFC3339, pluginMap["LastSuccessfulRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last successful run", "error", err)
							return
						}
						lastFailedRun, err = time.Parse(time.RFC3339, pluginMap["LastFailedRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last failed run", "error", err)
							return
						}
						lastCanceledRunJobUrl = pluginMap["LastCanceledRunJobUrl"].(string)
						lastCanceledRun, err = time.Parse(time.RFC3339, pluginMap["LastCanceledRun"].(string))
						if err != nil {
							logger.ErrorContext(workerCtx, "error parsing last canceled run", "error", err)
							return
						}
						totalRanVMs = int(pluginMap["TotalRanVMs"].(float64))
						totalSuccessfulRunsSinceStart = int(pluginMap["TotalSuccessfulRunsSinceStart"].(float64))
						totalFailedRunsSinceStart = int(pluginMap["TotalFailedRunsSinceStart"].(float64))
						totalCanceledRunsSinceStart = int(pluginMap["TotalCanceledRunsSinceStart"].(float64))
					}
					if repoName == "" {
						w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,owner=%s} %s\n", Name, ownerName, status)))
					} else {
						w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,owner=%s,repo=%s} %s\n", Name, ownerName, repoName, status)))
					}
					if !strings.Contains(pluginName, "_receiver") {
						if repoName == "" {
							w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))))
							w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))))
							w.Write([]byte(fmt.Sprintf("plugin_last_canceled_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))))
						} else {
							w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))))
							w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))))
							w.Write([]byte(fmt.Sprintf("plugin_last_canceled_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))))
						}
					}
					if repoName == "" {
						w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,owner=%s,status=%s} %s\n", Name, ownerName, status, statusSince.Format(time.RFC3339))))
					} else {
						w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,owner=%s,repo=%s,status=%s} %s\n", Name, ownerName, repoName, status, statusSince.Format(time.RFC3339))))
					}

					if strings.Contains(pluginName, "_receiver") {
						soloReceiver = true
					} else {
						soloReceiver = false
					}

					if !soloReceiver {
						w.Write([]byte(fmt.Sprintf("plugin_total_ran_vms{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalRanVMs)))
						w.Write([]byte(fmt.Sprintf("plugin_total_successful_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalSuccessfulRunsSinceStart)))
						w.Write([]byte(fmt.Sprintf("plugin_total_failed_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalFailedRunsSinceStart)))
						w.Write([]byte(fmt.Sprintf("plugin_total_canceled_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalCanceledRunsSinceStart)))
					}

					w.Write([]byte(fmt.Sprintf("host_cpu_count{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostCPUCount)))
					w.Write([]byte(fmt.Sprintf("host_cpu_used_count{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostCPUUsedCount)))
					w.Write([]byte(fmt.Sprintf("host_cpu_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostCPUUsagePercentage)))
					w.Write([]byte(fmt.Sprintf("host_memory_total_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryTotalBytes)))
					w.Write([]byte(fmt.Sprintf("host_memory_used_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryUsedBytes)))
					w.Write([]byte(fmt.Sprintf("host_memory_available_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryAvailableBytes)))
					w.Write([]byte(fmt.Sprintf("host_memory_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostMemoryUsagePercentage)))
					w.Write([]byte(fmt.Sprintf("host_disk_total_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskTotalBytes)))
					w.Write([]byte(fmt.Sprintf("host_disk_used_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskUsedBytes)))
					w.Write([]byte(fmt.Sprintf("host_disk_available_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskAvailableBytes)))
					w.Write([]byte(fmt.Sprintf("host_disk_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostDiskUsagePercentage)))
					w.Write([]byte(fmt.Sprintf("last_update{name=%s,owner=%s} %s\n", Name, ownerName, metricsData.LastUpdate.Format(time.RFC3339))))
				}
			}
			if nextCursor == 0 {
				break
			}
			cursor = nextCursor
		}
	}
}

func ExportMetricsToDB(pluginCtx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for {
			select {
			case <-pluginCtx.Done():
				return
			case <-ticker.C:
				logging.DevContext(pluginCtx, "Exporting metrics to database")
				ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting plugin from context", "error", err.Error())
				}
				databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting database client from context", "error", err.Error())
				}
				metricsData, err := GetMetricsDataFromContext(pluginCtx)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error getting metrics data from context", "error", err.Error())
				}
				metricsDataJson, err := json.Marshal(metricsData.MetricsData)
				if err != nil {
					logger.ErrorContext(pluginCtx, "error parsing metrics as json", "error", err.Error())
				}
				if pluginCtx.Err() == nil {
					// add last_update
					var metricsDataMap map[string]interface{}
					if err := json.Unmarshal(metricsDataJson, &metricsDataMap); err != nil {
						logger.ErrorContext(pluginCtx, "error unmarshalling metrics data", "error", err)
						return
					}
					metricsDataMap["last_update"] = time.Now()
					metricsDataJson, err = json.Marshal(metricsDataMap)
					if err != nil {
						logger.ErrorContext(pluginCtx, "error marshalling metrics data", "error", err)
						return
					}
					// This will create a single key using the first plugin's name. It will contain all plugin metrics though.
					setting := databaseContainer.Client.Set(pluginCtx, "anklet/metrics/"+ctxPlugin.Owner+"/"+ctxPlugin.Name, metricsDataJson, time.Hour*24*7) // keep metrics for one week max
					if setting.Err() != nil {
						logger.ErrorContext(pluginCtx, "error storing metrics data in Redis", "error", setting.Err())
						return
					}
					exists, err := databaseContainer.Client.Exists(pluginCtx, "anklet/metrics/"+ctxPlugin.Owner+"/"+ctxPlugin.Name).Result()
					if err != nil {
						logger.ErrorContext(pluginCtx, "error checking if key exists in Redis", "key", "anklet/metrics/"+ctxPlugin.Owner+"/"+ctxPlugin.Name, "error", err)
						return
					}
					if exists == 1 {
						logging.DevContext(pluginCtx, "successfully stored metrics data in Redis, key: anklet/metrics/"+ctxPlugin.Owner+"/"+ctxPlugin.Name+" exists: true")
					} else {
						logging.DevContext(pluginCtx, "successfully stored metrics data in Redis, key: anklet/metrics/"+ctxPlugin.Owner+"/"+ctxPlugin.Name+" exists: false")
					}
				}
			}
		}
	}()
}
