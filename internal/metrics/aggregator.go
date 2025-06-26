package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/veertuinc/anklet/internal/database"
)

// Start runs the HTTP server
func (s *Server) StartAggregatorServer(
	workerCtx context.Context,
	logger *slog.Logger,
	soloReceiver bool,
) {
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("ok"))
		if err != nil {
			logger.ErrorContext(workerCtx, "error writing response", "error", err)
		}
	})
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetricsV1(workerCtx, logger)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.HandleFunc("/metrics/v2", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			s.handleAggregatorJsonMetricsV2(workerCtx, logger)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handleAggregatorPrometheusMetrics(workerCtx, logger)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, err := w.Write([]byte("please use /metrics/v1"))
		if err != nil {
			logger.ErrorContext(workerCtx, "error writing response", "error", err)
		}
	})
	err := http.ListenAndServe(":"+s.Port, nil)
	if err != nil {
		logger.ErrorContext(workerCtx, "error starting aggregator server", "error", err)
		os.Exit(1)
	}
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
		err := json.NewEncoder(w).Encode(combinedMetrics)
		if err != nil {
			logger.ErrorContext(workerCtx, "error encoding combined metrics", "error", err)
			return
		}
	}
}

func (s *Server) handleAggregatorJsonMetricsV2(
	workerCtx context.Context,
	logger *slog.Logger,
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
		err := json.NewEncoder(w).Encode(combinedMetrics)
		if err != nil {
			logger.ErrorContext(workerCtx, "error encoding combined metrics", "error", err)
			return
		}
	}
}

func (s *Server) handleAggregatorPrometheusMetrics(
	workerCtx context.Context,
	logger *slog.Logger,
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
						_, err = fmt.Fprintf(w, "plugin_status{name=%s,owner=%s} %s\n", Name, ownerName, status)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 1", "error", err)
							return
						}
					} else {
						_, err = fmt.Fprintf(w, "plugin_status{name=%s,owner=%s,repo=%s} %s\n", Name, ownerName, repoName, status)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 2", "error", err)
							return
						}
					}
					if !strings.Contains(pluginName, "_receiver") {
						if repoName == "" {
							_, err = fmt.Fprintf(w, "plugin_last_successful_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 3", "error", err)
								return
							}
							_, err = fmt.Fprintf(w, "plugin_last_failed_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 4", "error", err)
								return
							}
							_, err = fmt.Fprintf(w, "plugin_last_canceled_run{name=%s,owner=%s,job_url=%s} %s\n", Name, ownerName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 5", "error", err)
								return
							}
						} else {
							_, err = fmt.Fprintf(w, "plugin_last_successful_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 6", "error", err)
								return
							}
							_, err = fmt.Fprintf(w, "plugin_last_failed_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 7", "error", err)
								return
							}
							_, err = fmt.Fprintf(w, "plugin_last_canceled_run{name=%s,owner=%s,repo=%s,job_url=%s} %s\n", Name, ownerName, repoName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))
							if err != nil {
								logger.ErrorContext(workerCtx, "error writing to writer 8", "error", err)
								return
							}
						}
					}
					if repoName == "" {
						_, err = fmt.Fprintf(w, "plugin_status_since{name=%s,owner=%s,status=%s} %s\n", Name, ownerName, status, statusSince.Format(time.RFC3339))
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 9", "error", err)
							return
						}
					} else {
						_, err = fmt.Fprintf(w, "plugin_status_since{name=%s,owner=%s,repo=%s,status=%s} %s\n", Name, ownerName, repoName, status, statusSince.Format(time.RFC3339))
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 10", "error", err)
							return
						}
					}

					if strings.Contains(pluginName, "_receiver") {
						soloReceiver = true
					} else {
						soloReceiver = false
					}

					if !soloReceiver {
						_, err = fmt.Fprintf(w, "plugin_total_ran_vms{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalRanVMs)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 11", "error", err)
							return
						}
						_, err = fmt.Fprintf(w, "plugin_total_successful_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalSuccessfulRunsSinceStart)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 12", "error", err)
							return
						}
						_, err = fmt.Fprintf(w, "plugin_total_failed_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalFailedRunsSinceStart)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 13", "error", err)
							return
						}
						_, err = fmt.Fprintf(w, "plugin_total_canceled_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", Name, pluginName, ownerName, totalCanceledRunsSinceStart)
						if err != nil {
							logger.ErrorContext(workerCtx, "error writing to writer 14", "error", err)
							return
						}
					}

					_, err = fmt.Fprintf(w, "host_cpu_count{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostCPUCount)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 15", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_cpu_used_count{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostCPUUsedCount)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 16", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_cpu_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostCPUUsagePercentage)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 17", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_memory_total_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryTotalBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 18", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_memory_used_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryUsedBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 19", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_memory_available_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostMemoryAvailableBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 20", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_memory_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostMemoryUsagePercentage)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 21", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_disk_total_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskTotalBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 22", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_disk_used_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskUsedBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 23", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_disk_available_bytes{name=%s,owner=%s} %d\n", Name, ownerName, metricsData.HostDiskAvailableBytes)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 24", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "host_disk_usage_percentage{name=%s,owner=%s} %f\n", Name, ownerName, metricsData.HostDiskUsagePercentage)
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 25", "error", err)
						return
					}
					_, err = fmt.Fprintf(w, "last_update{name=%s,owner=%s} %s\n", Name, ownerName, metricsData.LastUpdate.Format(time.RFC3339))
					if err != nil {
						logger.ErrorContext(workerCtx, "error writing to writer 26", "error", err)
						return
					}
				}
			}
			if nextCursor == 0 {
				break
			}
			cursor = nextCursor
		}
	}
}
