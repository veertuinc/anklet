package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

// Server defines the structure for the API server
type Server struct {
	Port string
}

type PluginBase struct {
	Name        string
	PluginName  string
	RepoName    string
	OwnerName   string
	Status      string
	StatusSince time.Time
}

type Plugin struct {
	*PluginBase
	LastSuccessfulRunJobUrl       string
	LastFailedRunJobUrl           string
	LastCanceledRunJobUrl         string
	LastSuccessfulRun             time.Time
	LastFailedRun                 time.Time
	LastCanceledRun               time.Time
	TotalRanVMs                   int
	TotalSuccessfulRunsSinceStart int
	TotalFailedRunsSinceStart     int
	TotalCanceledRunsSinceStart   int
}

type MetricsData struct {
	LastUpdate                    time.Time `json:"last_update"`
	TotalRunningVMs               int       `json:"total_running_vms"`
	TotalSuccessfulRunsSinceStart int       `json:"total_successful_runs_since_start"`
	TotalFailedRunsSinceStart     int       `json:"total_failed_runs_since_start"`
	TotalCanceledRunsSinceStart   int       `json:"total_canceled_runs_since_start"`
	HostCPUCount                  int       `json:"host_cpu_count"`
	HostCPUUsedCount              int       `json:"host_cpu_used_count"`
	HostCPUUsagePercentage        float64   `json:"host_cpu_usage_percentage"`
	HostMemoryTotalBytes          uint64    `json:"host_memory_total_bytes"`
	HostMemoryUsedBytes           uint64    `json:"host_memory_used_bytes"`
	HostMemoryAvailableBytes      uint64    `json:"host_memory_available_bytes"`
	HostMemoryUsagePercentage     float64   `json:"host_memory_usage_percentage"`
	HostDiskTotalBytes            uint64    `json:"host_disk_total_bytes"`
	HostDiskUsedBytes             uint64    `json:"host_disk_used_bytes"`
	HostDiskAvailableBytes        uint64    `json:"host_disk_available_bytes"`
	HostDiskUsagePercentage       float64   `json:"host_disk_usage_percentage"`
	Plugins                       []any     `json:"plugins"`
}

type MetricsDataLock struct {
	sync.RWMutex
	MetricsData
}

func (m *MetricsDataLock) AddPlugin(plugin any) error {
	m.Lock()
	defer m.Unlock()
	var pluginName string
	switch pluginTyped := plugin.(type) {
	case PluginBase:
		pluginName = pluginTyped.Name
	case Plugin:
		pluginName = pluginTyped.Name
	default:
		return fmt.Errorf("unable to get plugin name")
	}
	for _, plugin := range m.Plugins {
		var name string
		switch pluginTyped := plugin.(type) {
		case PluginBase:
			name = pluginTyped.Name
		case Plugin:
			name = pluginTyped.Name
		default:
			return fmt.Errorf("unable to get plugin name")
		}
		if name == pluginName { // already exists, don't do anything
			return nil
		}
	}
	m.Plugins = append(m.Plugins, plugin)
	return nil
}

func (m *MetricsDataLock) IncrementTotalRunningVMs(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	m.Lock()
	defer m.Unlock()
	m.TotalRunningVMs++
	m.IncrementPluginTotalRanVMs(workerCtx, pluginCtx)
}

func (m *MetricsDataLock) DecrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	if m.TotalRunningVMs > 0 {
		m.TotalRunningVMs--
	}
}

func (m *MetricsDataLock) IncrementTotalSuccessfulRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	m.Lock()
	defer m.Unlock()
	m.TotalSuccessfulRunsSinceStart++
	m.IncrementPluginTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx)
}

func (m *MetricsDataLock) IncrementTotalFailedRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	m.Lock()
	defer m.Unlock()
	m.TotalFailedRunsSinceStart++
	m.IncrementPluginTotalFailedRunsSinceStart(workerCtx, pluginCtx)
}

func (m *MetricsDataLock) IncrementTotalCanceledRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	m.Lock()
	defer m.Unlock()
	m.TotalCanceledRunsSinceStart++
	m.IncrementPluginTotalCanceledRunsSinceStart(workerCtx, pluginCtx)
}

func (m *MetricsDataLock) IncrementPluginTotalRanVMs(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return
	}
	for _, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.Name == pluginConfig.Name {
				err = UpdatePlugin(workerCtx, pluginCtx, Plugin{
					PluginBase: &PluginBase{
						Name: pluginConfig.Name,
					},
					TotalRanVMs: typedPlugin.TotalRanVMs + 1,
				})
				if err != nil {
					logging.Error(pluginCtx, "error updating plugin metrics", "error", err)
				}
			}
		}
	}
}

func (m *MetricsDataLock) IncrementPluginTotalSuccessfulRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return
	}
	for _, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.Name == pluginConfig.Name {
				err = UpdatePlugin(workerCtx, pluginCtx, Plugin{
					PluginBase: &PluginBase{
						Name: pluginConfig.Name,
					},
					TotalSuccessfulRunsSinceStart: typedPlugin.TotalSuccessfulRunsSinceStart + 1,
				})
				if err != nil {
					logging.Error(pluginCtx, "error updating plugin metrics", "error", err)
				}
			}
		}
	}
}

func (m *MetricsDataLock) IncrementPluginTotalFailedRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return
	}
	for _, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.Name == pluginConfig.Name {
				err = UpdatePlugin(workerCtx, pluginCtx, Plugin{
					PluginBase: &PluginBase{
						Name: pluginConfig.Name,
					},
					TotalFailedRunsSinceStart: typedPlugin.TotalFailedRunsSinceStart + 1,
				})
				if err != nil {
					logging.Error(pluginCtx, "error updating plugin metrics", "error", err)
				}
			}
		}
	}
}

func (m *MetricsDataLock) IncrementPluginTotalCanceledRunsSinceStart(
	workerCtx context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return
	}
	for _, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.Name == pluginConfig.Name {
				err = UpdatePlugin(workerCtx, pluginCtx, Plugin{
					PluginBase: &PluginBase{
						Name: pluginConfig.Name,
					},
					TotalCanceledRunsSinceStart: typedPlugin.TotalCanceledRunsSinceStart + 1,
				})
				if err != nil {
					logging.Error(pluginCtx, "error updating plugin metrics", "error", err)
				}
			}
		}
	}
}

func CompareAndUpdateMetrics(currentService any, updatedPlugin any) (any, error) {
	switch currentServiceTyped := currentService.(type) {
	case Plugin:
		updated, ok := updatedPlugin.(Plugin)
		if !ok {
			return nil, fmt.Errorf("unable to convert updatedPlugin to Plugin")
		}
		if updated.PluginName != "" {
			currentServiceTyped.PluginName = updated.PluginName
		}
		if updated.Status != "" {
			if currentServiceTyped.Status != updated.Status {
				currentServiceTyped.StatusSince = time.Now()
			}
			currentServiceTyped.Status = updated.Status
		}
		if !updated.LastSuccessfulRun.IsZero() {
			currentServiceTyped.LastSuccessfulRun = updated.LastSuccessfulRun
		}
		if !updated.LastFailedRun.IsZero() {
			currentServiceTyped.LastFailedRun = updated.LastFailedRun
		}
		if !updated.LastCanceledRun.IsZero() {
			currentServiceTyped.LastCanceledRun = updated.LastCanceledRun
		}
		if updated.LastSuccessfulRunJobUrl != "" {
			currentServiceTyped.LastSuccessfulRunJobUrl = updated.LastSuccessfulRunJobUrl
		}
		if updated.LastFailedRunJobUrl != "" {
			currentServiceTyped.LastFailedRunJobUrl = updated.LastFailedRunJobUrl
		}
		if updated.LastCanceledRunJobUrl != "" {
			currentServiceTyped.LastCanceledRunJobUrl = updated.LastCanceledRunJobUrl
		}
		if updated.TotalCanceledRunsSinceStart > currentServiceTyped.TotalCanceledRunsSinceStart {
			currentServiceTyped.TotalCanceledRunsSinceStart = updated.TotalCanceledRunsSinceStart
		}
		if updated.TotalFailedRunsSinceStart > currentServiceTyped.TotalFailedRunsSinceStart {
			currentServiceTyped.TotalFailedRunsSinceStart = updated.TotalFailedRunsSinceStart
		}
		if updated.TotalSuccessfulRunsSinceStart > currentServiceTyped.TotalSuccessfulRunsSinceStart {
			currentServiceTyped.TotalSuccessfulRunsSinceStart = updated.TotalSuccessfulRunsSinceStart
		}
		if updated.TotalRanVMs > currentServiceTyped.TotalRanVMs {
			currentServiceTyped.TotalRanVMs = updated.TotalRanVMs
		}
		return currentServiceTyped, nil
	case PluginBase:
		updated, ok := updatedPlugin.(PluginBase)
		if !ok {
			return nil, fmt.Errorf("unable to convert updatedPlugin to PluginBase")
		}
		if updated.PluginName != "" {
			currentServiceTyped.PluginName = updated.PluginName
		}
		if updated.Status != "" {
			if currentServiceTyped.Status != updated.Status {
				currentServiceTyped.StatusSince = time.Now()
			}
			currentServiceTyped.Status = updated.Status
		}
		return currentServiceTyped, nil
	default:
		return nil, fmt.Errorf("unable to convert currentService to Plugin or PluginBase")
	}
}

func UpdateSystemMetrics(pluginCtx context.Context, metricsData *MetricsDataLock) {
	cpuCount, err := cpu.Counts(false)
	if err != nil {
		logging.Error(pluginCtx, "Error getting CPU count", "error", err)
		metricsData.HostCPUCount = 0
	}
	metricsData.HostCPUCount = cpuCount
	cpuUsedPercent, err := cpu.Percent(0, false)
	if err != nil {
		logging.Error(pluginCtx, "Error getting CPU usage", "error", err)
		metricsData.HostCPUUsagePercentage = 0
		metricsData.HostCPUUsedCount = 0
		return
	}
	metricsData.HostCPUUsagePercentage = cpuUsedPercent[0]
	// Calculate CPU used count with more precision to avoid stagnation
	usedCount := float64(cpuCount) * (metricsData.HostCPUUsagePercentage / 100)
	metricsData.HostCPUUsedCount = int(usedCount)
	// MEMORY
	memStat, err := mem.VirtualMemory()
	if err != nil {
		logging.Error(pluginCtx, "Error getting memory usage", "error", err)
		metricsData.HostMemoryTotalBytes = 0
	}
	metricsData.HostMemoryTotalBytes = uint64(memStat.Total)
	metricsData.HostMemoryAvailableBytes = uint64(memStat.Available)
	metricsData.HostMemoryUsagePercentage = memStat.UsedPercent
	metricsData.HostMemoryUsedBytes = uint64(memStat.Used)
	// DISK
	diskStat, err := disk.Usage("/")
	if err != nil {
		logging.Error(pluginCtx, "Error getting disk usage", "error", err)
		metricsData.HostDiskUsagePercentage = 0
	}
	metricsData.HostDiskUsagePercentage = diskStat.UsedPercent
	metricsData.HostDiskTotalBytes = uint64(diskStat.Total)
	metricsData.HostDiskAvailableBytes = uint64(diskStat.Free)
	metricsData.HostDiskUsedBytes = uint64(diskStat.Used)
}

func UpdatePlugin(
	workerCtx context.Context,
	pluginCtx context.Context,
	updatedPlugin any,
) error {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	metricsData, err := GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return err
	}
	switch updatedPlugin.(type) {
	case Plugin:
		for i, currentPluginMetrics := range metricsData.Plugins {
			switch fullCurrentPluginMetrics := currentPluginMetrics.(type) {
			case Plugin:
				if fullCurrentPluginMetrics.Name == ctxPlugin.Name {
					newPlugin, err := CompareAndUpdateMetrics(currentPluginMetrics, updatedPlugin)
					if err != nil {
						return err
					}
					metricsData.Plugins[i] = newPlugin
				}
			}
		}
	case PluginBase:
		for i, currentPluginMetrics := range metricsData.Plugins {
			switch fullCurrentPluginMetrics := currentPluginMetrics.(type) {
			case PluginBase:
				if fullCurrentPluginMetrics.Name == ctxPlugin.Name {
					newPlugin, err := CompareAndUpdateMetrics(currentPluginMetrics, updatedPlugin)
					if err != nil {
						return err
					}
					metricsData.Plugins[i] = newPlugin
				}
			}
		}
	}
	return nil
}

func (m *MetricsDataLock) UpdatePlugin(
	workerCtx context.Context,
	pluginCtx context.Context,
	updatedPlugin any,
) error {
	m.Lock()
	defer m.Unlock()
	var name string
	switch fullUpdatedPlugin := updatedPlugin.(type) {
	case Plugin:
		if fullUpdatedPlugin.Name == "" {
			return fmt.Errorf("updatePlugin.Name is required")
		}
		for i, plugin := range m.Plugins {
			switch typedPlugin := plugin.(type) {
			case Plugin:
				if fullUpdatedPlugin.Name == typedPlugin.Name {
					newPlugin, err := CompareAndUpdateMetrics(typedPlugin, updatedPlugin)
					if err != nil {
						return err
					}
					m.Plugins[i] = newPlugin
				}
			}
		}
	case PluginBase:
		name = fullUpdatedPlugin.Name
		for i, plugin := range m.Plugins {
			switch typedPlugin := plugin.(type) {
			case PluginBase:
				if name == typedPlugin.Name {
					newPlugin, err := CompareAndUpdateMetrics(typedPlugin, updatedPlugin)
					if err != nil {
						return err
					}
					m.Plugins[i] = newPlugin
				}
			}
		}
	}
	return nil
}

func (m *MetricsDataLock) SetStatus(pluginCtx context.Context, status string) error {
	m.Lock()
	defer m.Unlock()
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	for i, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.Name == ctxPlugin.Name {
				if typedPlugin.Status != status { // only update status if it's changing
					typedPlugin.Status = status
					m.Plugins[i] = typedPlugin
					typedPlugin.StatusSince = time.Now()
				}
			}
		case PluginBase:
			if typedPlugin.Name == ctxPlugin.Name {
				if typedPlugin.Status != status { // only update status if it's changing
					typedPlugin.Status = status
					m.Plugins[i] = typedPlugin
					typedPlugin.StatusSince = time.Now()
				}
			}
		}
	}
	return nil
}

// NewServer creates a new instance of Server
func NewServer(port string) *Server {
	return &Server{
		Port: port,
	}
}

// Start runs the HTTP server
func (s *Server) Start(parentCtx context.Context, soloReceiver bool) {
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		// update system metrics each call
		metricsData, err := GetMetricsDataFromContext(parentCtx)
		if err != nil {
			http.Error(w, "failed to get metrics data", http.StatusInternalServerError)
			return
		}
		UpdateSystemMetrics(parentCtx, metricsData)
		//
		// if r.URL.Query().Get("format") == "json" {
		// 	s.handleJsonMetrics(parentCtx, soloReceiver)(w, r)
		// } else
		if r.URL.Query().Get("format") == "prometheus" {
			s.handlePrometheusMetrics(parentCtx, soloReceiver)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=prometheus'", http.StatusBadRequest)
		}
	})
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, err := w.Write([]byte("please use /metrics/v1"))
		if err != nil {
			logging.Error(parentCtx, "error writing response", "error", err)
		}
	})
	server := &http.Server{
		Addr: ":" + s.Port,
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			logging.Error(parentCtx, "error starting metrics server", "error", err)
		}
	}()

	// Wait for context cancellation
	<-parentCtx.Done()

	// Gracefully shutdown the server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := server.Shutdown(shutdownCtx)
	if err != nil {
		logging.Error(parentCtx, "error shutting down metrics server", "error", err)
	}
}

// handleMetrics processes the /metrics endpoint
// func (s *Server) handleJsonMetrics(ctx context.Context, soloReceiver bool) http.HandlerFunc {
// 	return func(w http.ResponseWriter, r *http.Request) {
// 		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
// 		w.Header().Set("Content-Type", "application/json")
// 		// json.NewEncoder(w).Encode(metricsData)
// 		customEncoder := json.NewEncoder(w)
// 		customEncoder.SetEscapeHTML(false)
// 		if soloReceiver {
// 			customEncoder.Encode(struct {
// 				HostCPUCount              int                      `json:"host_cpu_count"`
// 				HostCPUUsedCount          int                      `json:"host_cpu_used_count"`
// 				HostCPUUsagePercentage    float64                  `json:"host_cpu_usage_percentage"`
// 				HostMemoryTotalBytes      uint64                   `json:"host_memory_total_bytes"`
// 				HostMemoryUsedBytes       uint64                   `json:"host_memory_used_bytes"`
// 				HostMemoryAvailableBytes  uint64                   `json:"host_memory_available_bytes"`
// 				HostMemoryUsagePercentage float64                  `json:"host_memory_usage_percentage"`
// 				HostDiskTotalBytes        uint64                   `json:"host_disk_total_bytes"`
// 				HostDiskUsedBytes         uint64                   `json:"host_disk_used_bytes"`
// 				HostDiskAvailableBytes    uint64                   `json:"host_disk_available_bytes"`
// 				HostDiskUsagePercentage   float64                  `json:"host_disk_usage_percentage"`
// 				Plugins                   []map[string]any `json:"plugins"`
// 			}{
// 				HostCPUCount:              metricsData.HostCPUCount,
// 				HostCPUUsedCount:          metricsData.HostCPUUsedCount,
// 				HostCPUUsagePercentage:    metricsData.HostCPUUsagePercentage,
// 				HostMemoryTotalBytes:      metricsData.HostMemoryTotalBytes,
// 				HostMemoryUsedBytes:       metricsData.HostMemoryUsedBytes,
// 				HostMemoryAvailableBytes:  metricsData.HostMemoryAvailableBytes,
// 				HostMemoryUsagePercentage: metricsData.HostMemoryUsagePercentage,
// 				HostDiskTotalBytes:        metricsData.HostDiskTotalBytes,
// 				HostDiskUsedBytes:         metricsData.HostDiskUsedBytes,
// 				HostDiskAvailableBytes:    metricsData.HostDiskAvailableBytes,
// 				HostDiskUsagePercentage:   metricsData.HostDiskUsagePercentage,
// 				Plugins: func() []map[string]any {
// 					plugins := make([]map[string]any, len(metricsData.Plugins))
// 					for i, plugin := range metricsData.Plugins {
// 						pluginMap := make(map[string]any)
// 						switch s := plugin.(type) {
// 						case Plugin:
// 							pluginMap["name"] = s.Name
// 							pluginMap["plugin_name"] = s.PluginName
// 							if s.RepoName != "" {
// 								pluginMap["repo_name"] = s.RepoName
// 							}
// 							pluginMap["owner_name"] = s.OwnerName
// 							pluginMap["status"] = s.Status
// 							pluginMap["status_since"] = s.StatusSince
// 							pluginMap["last_successful_run_job_url"] = s.LastSuccessfulRunJobUrl
// 							pluginMap["last_failed_run_job_url"] = s.LastFailedRunJobUrl
// 							pluginMap["last_canceled_run_job_url"] = s.LastCanceledRunJobUrl
// 							pluginMap["last_successful_run"] = s.LastSuccessfulRun
// 							pluginMap["last_failed_run"] = s.LastFailedRun
// 							pluginMap["last_canceled_run"] = s.LastCanceledRun
// 							pluginMap["total_ran_vms"] = s.TotalRanVMs
// 							pluginMap["total_successful_runs_since_start"] = s.TotalSuccessfulRunsSinceStart
// 							pluginMap["total_failed_runs_since_start"] = s.TotalFailedRunsSinceStart
// 							pluginMap["total_canceled_runs_since_start"] = s.TotalCanceledRunsSinceStart
// 						case PluginBase:
// 							pluginMap["name"] = s.Name
// 							pluginMap["plugin_name"] = s.PluginName
// 							if s.RepoName != "" {
// 								pluginMap["repo_name"] = s.RepoName
// 							}
// 							pluginMap["owner_name"] = s.OwnerName
// 							pluginMap["status"] = s.Status
// 							pluginMap["status_since"] = s.StatusSince
// 						}
// 						plugins[i] = pluginMap
// 					}
// 					return plugins
// 				}(),
// 			})
// 		} else {
// 			customEncoder.Encode(struct {
// 				TotalRunningVMs               int                      `json:"total_running_vms"`
// 				TotalSuccessfulRunsSinceStart int                      `json:"total_successful_runs_since_start"`
// 				TotalFailedRunsSinceStart     int                      `json:"total_failed_runs_since_start"`
// 				TotalCanceledRunsSinceStart   int                      `json:"total_canceled_runs_since_start"`
// 				HostCPUCount                  int                      `json:"host_cpu_count"`
// 				HostCPUUsedCount              int                      `json:"host_cpu_used_count"`
// 				HostCPUUsagePercentage        float64                  `json:"host_cpu_usage_percentage"`
// 				HostMemoryTotalBytes          uint64                   `json:"host_memory_total_bytes"`
// 				HostMemoryUsedBytes           uint64                   `json:"host_memory_used_bytes"`
// 				HostMemoryAvailableBytes      uint64                   `json:"host_memory_available_bytes"`
// 				HostMemoryUsagePercentage     float64                  `json:"host_memory_usage_percentage"`
// 				HostDiskTotalBytes            uint64                   `json:"host_disk_total_bytes"`
// 				HostDiskUsedBytes             uint64                   `json:"host_disk_used_bytes"`
// 				HostDiskAvailableBytes        uint64                   `json:"host_disk_available_bytes"`
// 				HostDiskUsagePercentage       float64                  `json:"host_disk_usage_percentage"`
// 				Plugins                       []map[string]any `json:"plugins"`
// 			}{
// 				TotalRunningVMs:               metricsData.TotalRunningVMs,
// 				TotalSuccessfulRunsSinceStart: metricsData.TotalSuccessfulRunsSinceStart,
// 				TotalFailedRunsSinceStart:     metricsData.TotalFailedRunsSinceStart,
// 				TotalCanceledRunsSinceStart:   metricsData.TotalCanceledRunsSinceStart,
// 				HostCPUCount:                  metricsData.HostCPUCount,
// 				HostCPUUsedCount:              metricsData.HostCPUUsedCount,
// 				HostCPUUsagePercentage:        metricsData.HostCPUUsagePercentage,
// 				HostMemoryTotalBytes:          metricsData.HostMemoryTotalBytes,
// 				HostMemoryUsedBytes:           metricsData.HostMemoryUsedBytes,
// 				HostMemoryAvailableBytes:      metricsData.HostMemoryAvailableBytes,
// 				HostMemoryUsagePercentage:     metricsData.HostMemoryUsagePercentage,
// 				HostDiskTotalBytes:            metricsData.HostDiskTotalBytes,
// 				HostDiskUsedBytes:             metricsData.HostDiskUsedBytes,
// 				HostDiskAvailableBytes:        metricsData.HostDiskAvailableBytes,
// 				HostDiskUsagePercentage:       metricsData.HostDiskUsagePercentage,
// 				Plugins: func() []map[string]any {
// 					plugins := make([]map[string]any, len(metricsData.Plugins))
// 					for i, plugin := range metricsData.Plugins {
// 						pluginMap := make(map[string]any)
// 						switch s := plugin.(type) {
// 						case Plugin:
// 							pluginMap["name"] = s.Name
// 							pluginMap["plugin_name"] = s.PluginName
// 							if s.RepoName != "" {
// 								pluginMap["repo_name"] = s.RepoName
// 							}
// 							pluginMap["owner_name"] = s.OwnerName
// 							pluginMap["status"] = s.Status
// 							pluginMap["status_since"] = s.StatusSince
// 							pluginMap["last_successful_run_job_url"] = s.LastSuccessfulRunJobUrl
// 							pluginMap["last_failed_run_job_url"] = s.LastFailedRunJobUrl
// 							pluginMap["last_successful_run"] = s.LastSuccessfulRun
// 							pluginMap["last_failed_run"] = s.LastFailedRun
// 							pluginMap["last_canceled_run_job_url"] = s.LastCanceledRunJobUrl
// 							pluginMap["last_canceled_run"] = s.LastCanceledRun
// 							pluginMap["total_ran_vms"] = s.TotalRanVMs
// 							pluginMap["total_successful_runs_since_start"] = s.TotalSuccessfulRunsSinceStart
// 							pluginMap["total_failed_runs_since_start"] = s.TotalFailedRunsSinceStart
// 							pluginMap["total_canceled_runs_since_start"] = s.TotalCanceledRunsSinceStart
// 						case PluginBase:
// 							pluginMap["name"] = s.Name
// 							pluginMap["plugin_name"] = s.PluginName
// 							if s.RepoName != "" {
// 								pluginMap["repo_name"] = s.RepoName
// 							}
// 							pluginMap["owner_name"] = s.OwnerName
// 							pluginMap["status"] = s.Status
// 							pluginMap["status_since"] = s.StatusSince
// 						}
// 						plugins[i] = pluginMap
// 					}
// 					return plugins
// 				}(),
// 			})
// 		}
// 	}
// }

func (s *Server) handlePrometheusMetrics(ctx context.Context, soloReceiver bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
		w.Header().Set("Content-Type", "text/plain")
		if !soloReceiver {
			_, err := fmt.Fprintf(w, "total_running_vms %d\n", metricsData.TotalRunningVMs)
			if err != nil {
				panic(err)
			}
			_, err = fmt.Fprintf(w, "total_successful_runs_since_start %d\n", metricsData.TotalSuccessfulRunsSinceStart)
			if err != nil {
				panic(err)
			}
			_, err = fmt.Fprintf(w, "total_failed_runs_since_start %d\n", metricsData.TotalFailedRunsSinceStart)
			if err != nil {
				panic(err)
			}
			_, err = fmt.Fprintf(w, "total_canceled_runs_since_start %d\n", metricsData.TotalCanceledRunsSinceStart)
			if err != nil {
				panic(err)
			}
		}
		for _, service := range metricsData.Plugins {
			var name string
			var pluginName string
			var repoName string
			var ownerName string
			var status string
			var StatusSince time.Time
			var lastSuccessfulRun time.Time
			var lastFailedRun time.Time
			var lastCanceledRun time.Time
			var lastSuccessfulRunJobUrl string
			var lastFailedRunJobUrl string
			var lastCanceledRunJobUrl string
			var totalRanVMs int
			var totalSuccessfulRunsSinceStart int
			var totalFailedRunsSinceStart int
			var totalCanceledRunsSinceStart int
			switch plugin := service.(type) {
			case Plugin:
				name = plugin.Name
				pluginName = plugin.PluginName
				if plugin.RepoName != "" {
					repoName = plugin.RepoName
				}
				ownerName = plugin.OwnerName
				status = plugin.Status
				StatusSince = plugin.StatusSince
				lastSuccessfulRun = plugin.LastSuccessfulRun
				lastFailedRun = plugin.LastFailedRun
				lastCanceledRun = plugin.LastCanceledRun
				lastSuccessfulRunJobUrl = plugin.LastSuccessfulRunJobUrl
				lastFailedRunJobUrl = plugin.LastFailedRunJobUrl
				lastCanceledRunJobUrl = plugin.LastCanceledRunJobUrl
				totalRanVMs = plugin.TotalRanVMs
				totalSuccessfulRunsSinceStart = plugin.TotalSuccessfulRunsSinceStart
				totalFailedRunsSinceStart = plugin.TotalFailedRunsSinceStart
				totalCanceledRunsSinceStart = plugin.TotalCanceledRunsSinceStart
			case PluginBase:
				name = plugin.Name
				pluginName = plugin.PluginName
				if plugin.RepoName != "" {
					repoName = plugin.RepoName
				}
				ownerName = plugin.OwnerName
				status = plugin.Status
				StatusSince = plugin.StatusSince
			default:
				panic("unable to convert plugin to Plugin or PluginBase")
			}
			if repoName == "" {
				_, err := fmt.Fprintf(w, "plugin_status{name=%s,plugin=%s,owner=%s} %s\n", name, pluginName, ownerName, status)
				if err != nil {
					panic(err)
				}
			} else {
				_, err := fmt.Fprintf(w, "plugin_status{name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, status)
				if err != nil {
					panic(err)
				}
			}
			if !strings.Contains(pluginName, "_receiver") {
				if repoName == "" {
					_, err := fmt.Fprintf(w, "plugin_last_successful_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
					_, err = fmt.Fprintf(w, "plugin_last_failed_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
					_, err = fmt.Fprintf(w, "plugin_last_canceled_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
				} else {
					_, err := fmt.Fprintf(w, "plugin_last_successful_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
					_, err = fmt.Fprintf(w, "plugin_last_failed_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
					_, err = fmt.Fprintf(w, "plugin_last_canceled_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))
					if err != nil {
						panic(err)
					}
				}
			}
			if repoName == "" {
				_, err := fmt.Fprintf(w, "plugin_status_since{name=%s,plugin=%s,owner=%s} %s\n", name, pluginName, ownerName, StatusSince.Format(time.RFC3339))
				if err != nil {
					panic(err)
				}
			} else {
				_, err := fmt.Fprintf(w, "plugin_status_since{name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, StatusSince.Format(time.RFC3339))
				if err != nil {
					panic(err)
				}
			}
			if !strings.Contains(pluginName, "_receiver") {
				_, err := fmt.Fprintf(w, "plugin_total_ran_vms{name=%s,plugin=%s,owner=%s} %d\n", name, pluginName, ownerName, totalRanVMs)
				if err != nil {
					panic(err)
				}
				_, err = fmt.Fprintf(w, "plugin_total_successful_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", name, pluginName, ownerName, totalSuccessfulRunsSinceStart)
				if err != nil {
					panic(err)
				}
				_, err = fmt.Fprintf(w, "plugin_total_failed_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", name, pluginName, ownerName, totalFailedRunsSinceStart)
				if err != nil {
					panic(err)
				}
				_, err = fmt.Fprintf(w, "plugin_total_canceled_runs_since_start{name=%s,plugin=%s,owner=%s} %d\n", name, pluginName, ownerName, totalCanceledRunsSinceStart)
				if err != nil {
					panic(err)
				}
			}
		}
		_, err := fmt.Fprintf(w, "host_cpu_count %d\n", metricsData.HostCPUCount)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_cpu_used_count %d\n", metricsData.HostCPUUsedCount)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_cpu_usage_percentage %f\n", metricsData.HostCPUUsagePercentage)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_memory_total_bytes %d\n", metricsData.HostMemoryTotalBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_memory_used_bytes %d\n", metricsData.HostMemoryUsedBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_memory_available_bytes %d\n", metricsData.HostMemoryAvailableBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_memory_usage_percentage %f\n", metricsData.HostMemoryUsagePercentage)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_disk_total_bytes %d\n", metricsData.HostDiskTotalBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_disk_used_bytes %d\n", metricsData.HostDiskUsedBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_disk_available_bytes %d\n", metricsData.HostDiskAvailableBytes)
		if err != nil {
			panic(err)
		}
		_, err = fmt.Fprintf(w, "host_disk_usage_percentage %f\n", metricsData.HostDiskUsagePercentage)
		if err != nil {
			panic(err)
		}
	}
}

func GetMetricsDataFromContext(ctx context.Context) (*MetricsDataLock, error) {
	metricsData, ok := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
	if !ok {
		return nil, fmt.Errorf("GetMetricsDataFromContext failed")
	}
	return metricsData, nil
}

func Cleanup(ctx context.Context, owner string, name string) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		logging.Error(ctx, "error getting database client from context", "error", err.Error())
	}
	_, result := databaseContainer.RetryDel(context.Background(), "anklet/metrics/"+owner+"/"+name)
	if result != nil {
		logging.Error(ctx, "error deleting metrics data from Redis", "error", result.Error())
	}
	// logging.Dev(ctx, "successfully deleted metrics data from Redis, key: anklet/metrics/"+owner+"/"+name)
}

func ExportMetricsToDB(workerCtx context.Context, pluginCtx context.Context, keyEnding string) {
	logging.Info(pluginCtx, "starting ExportMetricsToDB", "keyEnding", keyEnding)
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting database client from context", "error", err)
	}
	ticker := time.NewTicker(10 * time.Second)
	amountOfErrorsAllowed := 60
	atLeastOneRun := false
	metricsKey := "anklet/metrics/" + keyEnding
	go func() {
		for {
			metricsData, err := GetMetricsDataFromContext(workerCtx)
			if err != nil {
				logging.Error(pluginCtx, "error getting metrics data from context", "error", err.Error())
			}
			// Update system metrics before exporting
			UpdateSystemMetrics(pluginCtx, metricsData)
			metricsDataJson, err := json.Marshal(metricsData.MetricsData)
			if err != nil {
				logging.Error(pluginCtx, "error parsing metrics as json", "error", err.Error())
			}
			select {
			case <-pluginCtx.Done():
				return
			default:
				if pluginCtx.Err() == nil {
					// add last_update
					var metricsDataMap map[string]any
					if err := json.Unmarshal(metricsDataJson, &metricsDataMap); err != nil {
						logging.Error(pluginCtx, "error unmarshalling metrics data", "error", err)
						amountOfErrorsAllowed--
						if amountOfErrorsAllowed == 0 {
							os.Exit(1)
						}
						continue
					}
					metricsDataMap["last_update"] = time.Now()
					metricsDataJson, err = json.Marshal(metricsDataMap)
					if err != nil {
						logging.Error(pluginCtx, "error marshalling metrics data", "error", err)
						amountOfErrorsAllowed--
						if amountOfErrorsAllowed == 0 {
							os.Exit(1)
						}
						continue
					}
					// This will create a single key using the first plugin's name. It will contain all plugin metrics though.
					err = databaseContainer.RetrySet(pluginCtx, metricsKey, metricsDataJson, time.Hour*24*7) // keep metrics for one week max
					if err != nil {
						logging.Error(pluginCtx, "error storing metrics data in Redis", "error", err)
						amountOfErrorsAllowed--
						if amountOfErrorsAllowed == 0 {
							os.Exit(1)
						}
						continue
					}
					_, err = databaseContainer.RetryExists(pluginCtx, metricsKey)
					if err != nil {
						logging.Error(pluginCtx, "error checking if key exists in Redis", "key", metricsKey, "error", err)
						amountOfErrorsAllowed--
						if amountOfErrorsAllowed == 0 {
							os.Exit(1)
						}
						continue
					}
					if amountOfErrorsAllowed < 60 {
						logging.Info(pluginCtx, "errors resolved with metrics export")
						amountOfErrorsAllowed = 60
					}
					if !atLeastOneRun {
						atLeastOneRun = true
					}
					<-ticker.C
				}
			}
		}
	}()
	for !atLeastOneRun {
		time.Sleep(time.Second * 1)
	}
}
