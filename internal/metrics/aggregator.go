package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/veertuinc/anklet/internal/database"
)

// AggregatorHandler returns an isolated http.Handler for the metrics aggregator server.
func (s *Server) AggregatorHandler(workerCtx context.Context, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthcheck", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			logger.DebugContext(workerCtx, "error writing healthcheck response", "error", err)
		}
	})
	mux.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("format") {
		case "json":
			s.handleAggregatorJsonMetricsV1(workerCtx, logger)(w, r)
		case "prometheus":
			s.handleAggregatorPrometheusMetrics(workerCtx, logger)(w, r)
		default:
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/metrics/v2", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("format") {
		case "json":
			s.handleAggregatorJsonMetricsV2(workerCtx, logger)(w, r)
		case "prometheus":
			s.handleAggregatorPrometheusMetrics(workerCtx, logger)(w, r)
		default:
			http.Error(w, "unsupported format, please use '?format=json' or '?format=prometheus'", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		if _, err := w.Write([]byte("please use /metrics/v1")); err != nil {
			logger.DebugContext(workerCtx, "error writing /metrics response", "error", err)
		}
	})
	return mux
}

// StartAggregatorServer opens a listener and runs the aggregator HTTP server until workerCtx cancels.
func (s *Server) StartAggregatorServer(
	workerCtx context.Context,
	logger *slog.Logger,
	_ bool,
) {
	ln, err := net.Listen("tcp", s.listenAddr())
	if err != nil {
		logger.ErrorContext(workerCtx, "error listening for aggregator server", "addr", s.listenAddr(), "error", err)
		return
	}
	s.StartAggregatorServerWithListener(workerCtx, logger, ln)
}

// StartAggregatorServerWithListener runs the aggregator HTTP server on an already-bound listener.
func (s *Server) StartAggregatorServerWithListener(
	workerCtx context.Context,
	logger *slog.Logger,
	ln net.Listener,
) {
	server := newHTTPServer(ln.Addr().String(), s.AggregatorHandler(workerCtx, logger))

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			logger.ErrorContext(workerCtx, "error starting aggregator server", "error", err)
		}
	}()

	<-workerCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.ErrorContext(workerCtx, "error shutting down aggregator server", "error", err)
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

type aggregatedEntry struct {
	response   jsonMetricsResponse
	isReceiver bool
}

func collectAggregatedMetrics(
	ctx context.Context,
	logger *slog.Logger,
) ([]aggregatedEntry, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting database client from context: %w", err)
	}

	keys, err := databaseContainer.ScanAllKeys(ctx, "anklet/metrics/*")
	if err != nil {
		return nil, fmt.Errorf("scanning database for metrics: %w", err)
	}

	entries := make([]aggregatedEntry, 0, len(keys))
	for _, key := range keys {
		value, err := databaseContainer.RetryGet(ctx, key)
		if err != nil {
			logger.WarnContext(ctx, "skipping metrics key due to Redis get error", "key", key, "error", err)
			continue
		}
		var metricsData MetricsData
		if err := json.Unmarshal([]byte(value), &metricsData); err != nil {
			logger.WarnContext(ctx, "skipping metrics key due to JSON unmarshal error", "key", key, "error", err)
			continue
		}
		for _, plugin := range metricsData.Plugins {
			isRecv := plugin.isReceiverPlugin()
			resp := jsonMetricsResponse{
				RepoName:                  plugin.RepoName,
				PluginName:                plugin.PluginName,
				CustomName:                plugin.Name,
				OwnerName:                 plugin.OwnerName,
				LastUpdate:                metricsData.LastUpdate.Format(time.RFC3339),
				PluginStatus:              plugin.Status,
				PluginStatusSince:         plugin.StatusSince.Format(time.RFC3339),
				HostCPUCount:              metricsData.HostCPUCount,
				HostCPUUsedCount:          metricsData.HostCPUUsedCount,
				HostCPUUsagePercentage:    metricsData.HostCPUUsagePercentage,
				HostMemoryTotalBytes:      int64(metricsData.HostMemoryTotalBytes),
				HostMemoryUsedBytes:       int64(metricsData.HostMemoryUsedBytes),
				HostMemoryAvailableBytes:  int64(metricsData.HostMemoryAvailableBytes),
				HostMemoryUsagePercentage: metricsData.HostMemoryUsagePercentage,
				HostDiskTotalBytes:        int64(metricsData.HostDiskTotalBytes),
				HostDiskUsedBytes:         int64(metricsData.HostDiskUsedBytes),
				HostDiskAvailableBytes:    int64(metricsData.HostDiskAvailableBytes),
				HostDiskUsagePercentage:   metricsData.HostDiskUsagePercentage,
			}
			if !isRecv {
				resp.PluginLastSuccessfulRun = plugin.LastSuccessfulRun.Format(time.RFC3339)
				resp.PluginLastFailedRun = plugin.LastFailedRun.Format(time.RFC3339)
				resp.PluginLastCanceledRun = plugin.LastCanceledRun.Format(time.RFC3339)
				resp.PluginTotalRanVMs = plugin.TotalRanVMs
				resp.PluginTotalSuccessfulRunsSinceStart = plugin.TotalSuccessfulRunsSinceStart
				resp.PluginTotalFailedRunsSinceStart = plugin.TotalFailedRunsSinceStart
				resp.PluginTotalCanceledRunsSinceStart = plugin.TotalCanceledRunsSinceStart
				resp.PluginLastSuccessfulRunJobUrl = plugin.LastSuccessfulRunJobUrl
				resp.PluginLastFailedRunJobUrl = plugin.LastFailedRunJobUrl
				resp.PluginLastCanceledRunJobUrl = plugin.LastCanceledRunJobUrl
			} else {
				zeroTime := time.Time{}.Format(time.RFC3339)
				resp.PluginLastSuccessfulRun = zeroTime
				resp.PluginLastFailedRun = zeroTime
				resp.PluginLastCanceledRun = zeroTime
			}
			entries = append(entries, aggregatedEntry{
				response:   resp,
				isReceiver: isRecv,
			})
		}
	}
	return entries, nil
}

func (s *Server) handleAggregatorJsonMetricsV1(
	workerCtx context.Context,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		entries, err := collectAggregatedMetrics(workerCtx, logger)
		if err != nil {
			logger.ErrorContext(workerCtx, "error collecting aggregated metrics", "error", err)
			http.Error(w, "failed to collect metrics", http.StatusInternalServerError)
			return
		}
		combinedMetrics := make(map[string]jsonMetricsResponse, len(entries))
		for _, entry := range entries {
			combinedMetrics[entry.response.CustomName] = entry.response
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(combinedMetrics); err != nil {
			logger.ErrorContext(workerCtx, "error encoding combined metrics", "error", err)
		}
	}
}

func (s *Server) handleAggregatorJsonMetricsV2(
	workerCtx context.Context,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		entries, err := collectAggregatedMetrics(workerCtx, logger)
		if err != nil {
			logger.ErrorContext(workerCtx, "error collecting aggregated metrics", "error", err)
			http.Error(w, "failed to collect metrics", http.StatusInternalServerError)
			return
		}
		combinedMetrics := make([]jsonMetricsResponse, 0, len(entries))
		for _, entry := range entries {
			combinedMetrics = append(combinedMetrics, entry.response)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(combinedMetrics); err != nil {
			logger.ErrorContext(workerCtx, "error encoding combined metrics", "error", err)
		}
	}
}

func (s *Server) handleAggregatorPrometheusMetrics(
	workerCtx context.Context,
	logger *slog.Logger,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		entries, err := collectAggregatedMetrics(workerCtx, logger)
		if err != nil {
			logger.ErrorContext(workerCtx, "error collecting aggregated metrics", "error", err)
			http.Error(w, "failed to collect metrics", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		for _, entry := range entries {
			m := entry.response
			name := sanitizeLabel(m.CustomName)
			pluginName := sanitizeLabel(m.PluginName)
			ownerName := sanitizeLabel(m.OwnerName)
			repoName := sanitizeLabel(m.RepoName)
			status := sanitizeLabel(m.PluginStatus)

			scopeLabels := fmt.Sprintf("name=%s,owner=%s", name, ownerName)
			if repoName != "" {
				scopeLabels += ",repo=" + repoName
			}

			lines := []string{
				fmt.Sprintf("plugin_status{%s} %s\n", scopeLabels, status),
			}
			if !entry.isReceiver {
				lines = append(lines,
					fmt.Sprintf("plugin_last_successful_run{%s,job_url=%s} %s\n", scopeLabels, sanitizeLabel(m.PluginLastSuccessfulRunJobUrl), m.PluginLastSuccessfulRun),
					fmt.Sprintf("plugin_last_failed_run{%s,job_url=%s} %s\n", scopeLabels, sanitizeLabel(m.PluginLastFailedRunJobUrl), m.PluginLastFailedRun),
					fmt.Sprintf("plugin_last_canceled_run{%s,job_url=%s} %s\n", scopeLabels, sanitizeLabel(m.PluginLastCanceledRunJobUrl), m.PluginLastCanceledRun),
				)
			}
			lines = append(lines, fmt.Sprintf("plugin_status_since{%s,status=%s} %s\n", scopeLabels, status, m.PluginStatusSince))

			if !entry.isReceiver {
				counterLabels := fmt.Sprintf("name=%s,plugin=%s,owner=%s", name, pluginName, ownerName)
				lines = append(lines,
					fmt.Sprintf("plugin_total_ran_vms{%s} %d\n", counterLabels, m.PluginTotalRanVMs),
					fmt.Sprintf("plugin_total_successful_runs_since_start{%s} %d\n", counterLabels, m.PluginTotalSuccessfulRunsSinceStart),
					fmt.Sprintf("plugin_total_failed_runs_since_start{%s} %d\n", counterLabels, m.PluginTotalFailedRunsSinceStart),
					fmt.Sprintf("plugin_total_canceled_runs_since_start{%s} %d\n", counterLabels, m.PluginTotalCanceledRunsSinceStart),
				)
			}

			hostLabels := fmt.Sprintf("name=%s,owner=%s", name, ownerName)
			lines = append(lines,
				fmt.Sprintf("host_cpu_count{%s} %d\n", hostLabels, m.HostCPUCount),
				fmt.Sprintf("host_cpu_used_count{%s} %d\n", hostLabels, m.HostCPUUsedCount),
				fmt.Sprintf("host_cpu_usage_percentage{%s} %f\n", hostLabels, m.HostCPUUsagePercentage),
				fmt.Sprintf("host_memory_total_bytes{%s} %d\n", hostLabels, m.HostMemoryTotalBytes),
				fmt.Sprintf("host_memory_used_bytes{%s} %d\n", hostLabels, m.HostMemoryUsedBytes),
				fmt.Sprintf("host_memory_available_bytes{%s} %d\n", hostLabels, m.HostMemoryAvailableBytes),
				fmt.Sprintf("host_memory_usage_percentage{%s} %f\n", hostLabels, m.HostMemoryUsagePercentage),
				fmt.Sprintf("host_disk_total_bytes{%s} %d\n", hostLabels, m.HostDiskTotalBytes),
				fmt.Sprintf("host_disk_used_bytes{%s} %d\n", hostLabels, m.HostDiskUsedBytes),
				fmt.Sprintf("host_disk_available_bytes{%s} %d\n", hostLabels, m.HostDiskAvailableBytes),
				fmt.Sprintf("host_disk_usage_percentage{%s} %f\n", hostLabels, m.HostDiskUsagePercentage),
				fmt.Sprintf("last_update{%s} %s\n", hostLabels, m.LastUpdate),
			)

			for _, line := range lines {
				if _, writeErr := w.Write([]byte(line)); writeErr != nil {
					logger.DebugContext(workerCtx, "error writing aggregated prometheus metrics", "error", writeErr)
					return
				}
			}
		}
	}
}
