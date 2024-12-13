package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veertuinc/anklet/internal/config"
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
	LastSuccessfulRunJobUrl string
	LastFailedRunJobUrl     string
	LastCanceledRunJobUrl   string
	LastSuccessfulRun       time.Time
	LastFailedRun           time.Time
	LastCanceledRun         time.Time
}

type MetricsData struct {
	TotalRunningVMs               int           `json:"total_running_vms"`
	TotalSuccessfulRunsSinceStart int           `json:"total_successful_runs_since_start"`
	TotalFailedRunsSinceStart     int           `json:"total_failed_runs_since_start"`
	TotalCanceledRunsSinceStart   int           `json:"total_canceled_runs_since_start"`
	HostCPUCount                  int           `json:"host_cpu_count"`
	HostCPUUsedCount              int           `json:"host_cpu_used_count"`
	HostCPUUsagePercentage        float64       `json:"host_cpu_usage_percentage"`
	HostMemoryTotalBytes          uint64        `json:"host_memory_total_bytes"`
	HostMemoryUsedBytes           uint64        `json:"host_memory_used_bytes"`
	HostMemoryAvailableBytes      uint64        `json:"host_memory_available_bytes"`
	HostMemoryUsagePercentage     float64       `json:"host_memory_usage_percentage"`
	HostDiskTotalBytes            uint64        `json:"host_disk_total_bytes"`
	HostDiskUsedBytes             uint64        `json:"host_disk_used_bytes"`
	HostDiskAvailableBytes        uint64        `json:"host_disk_available_bytes"`
	HostDiskUsagePercentage       float64       `json:"host_disk_usage_percentage"`
	Plugins                       []interface{} `json:"plugins"`
}

type MetricsDataLock struct {
	sync.RWMutex
	MetricsData
}

func (m *MetricsDataLock) AddPlugin(plugin interface{}) error {
	m.Lock()
	defer m.Unlock()
	var pluginName string
	switch pluginTyped := plugin.(type) {
	case PluginBase:
		pluginName = pluginTyped.Name
	case Plugin:
		pluginName = pluginTyped.PluginBase.Name
	default:
		return fmt.Errorf("unable to get plugin name")
	}
	for _, plugin := range m.Plugins {
		var name string
		switch pluginTyped := plugin.(type) {
		case PluginBase:
			name = pluginTyped.Name
		case Plugin:
			name = pluginTyped.PluginBase.Name
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

func (m *MetricsDataLock) IncrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	m.TotalRunningVMs++
}

func (m *MetricsDataLock) DecrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	if m.TotalRunningVMs > 0 {
		m.TotalRunningVMs--
	}
}

func (m *MetricsDataLock) IncrementTotalSuccessfulRunsSinceStart() {
	m.Lock()
	defer m.Unlock()
	m.TotalSuccessfulRunsSinceStart++
}

func (m *MetricsDataLock) IncrementTotalFailedRunsSinceStart() {
	m.Lock()
	defer m.Unlock()
	m.TotalFailedRunsSinceStart++
}

func (m *MetricsDataLock) IncrementTotalCanceledRunsSinceStart() {
	m.Lock()
	defer m.Unlock()
	m.TotalCanceledRunsSinceStart++
}

func CompareAndUpdateMetrics(currentService interface{}, updatedPlugin interface{}) (interface{}, error) {
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
		if updated.LastSuccessfulRunJobUrl != "" {
			currentServiceTyped.LastSuccessfulRunJobUrl = updated.LastSuccessfulRunJobUrl
		}
		if updated.LastFailedRunJobUrl != "" {
			currentServiceTyped.LastFailedRunJobUrl = updated.LastFailedRunJobUrl
		}
		if updated.LastCanceledRunJobUrl != "" {
			currentServiceTyped.LastCanceledRunJobUrl = updated.LastCanceledRunJobUrl
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

func UpdateSystemMetrics(pluginCtx context.Context, logger *slog.Logger, metricsData *MetricsDataLock) {
	cpuCount, err := cpu.Counts(false)
	if err != nil {
		logger.ErrorContext(pluginCtx, "Error getting CPU count", "error", err)
		metricsData.HostCPUCount = 0
	}
	metricsData.HostCPUCount = cpuCount
	cpuUsedPercent, err := cpu.Percent(0, false)
	if err != nil {
		logger.ErrorContext(pluginCtx, "Error getting CPU usage", "error", err)
		metricsData.HostCPUUsagePercentage = 0
	}
	metricsData.HostCPUUsagePercentage = cpuUsedPercent[0]
	metricsData.HostCPUUsedCount = int(float64(cpuCount) * (metricsData.HostCPUUsagePercentage / 100))
	// MEMORY
	memStat, err := mem.VirtualMemory()
	if err != nil {
		logger.ErrorContext(pluginCtx, "Error getting memory usage", "error", err)
		metricsData.HostMemoryTotalBytes = 0
	}
	metricsData.HostMemoryTotalBytes = uint64(memStat.Total)
	metricsData.HostMemoryAvailableBytes = uint64(memStat.Available)
	metricsData.HostMemoryUsagePercentage = memStat.UsedPercent
	metricsData.HostMemoryUsedBytes = uint64(memStat.Used)
	// DISK
	diskStat, err := disk.Usage("/")
	if err != nil {
		logger.ErrorContext(pluginCtx, "Error getting disk usage", "error", err)
		metricsData.HostDiskUsagePercentage = 0
	}
	metricsData.HostDiskUsagePercentage = diskStat.UsedPercent
	metricsData.HostDiskTotalBytes = uint64(diskStat.Total)
	metricsData.HostDiskAvailableBytes = uint64(diskStat.Free)
	metricsData.HostDiskUsedBytes = uint64(diskStat.Used)
}

func UpdatePlugin(workerCtx context.Context, pluginCtx context.Context, logger *slog.Logger, updatedPlugin interface{}) error {
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
				if fullCurrentPluginMetrics.PluginBase.Name == ctxPlugin.Name {
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

func (m *MetricsDataLock) UpdatePlugin(pluginCtx context.Context, logger *slog.Logger, updatedPlugin interface{}) error {
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

func (m *MetricsDataLock) SetStatus(pluginCtx context.Context, logger *slog.Logger, status string) error {
	m.Lock()
	defer m.Unlock()
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	for i, plugin := range m.Plugins {
		switch typedPlugin := plugin.(type) {
		case Plugin:
			if typedPlugin.PluginBase.Name == ctxPlugin.Name {
				typedPlugin.Status = status
				m.Plugins[i] = typedPlugin
			}
		case PluginBase:
			if typedPlugin.Name == ctxPlugin.Name {
				typedPlugin.Status = status
				m.Plugins[i] = typedPlugin
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
func (s *Server) Start(parentCtx context.Context, logger *slog.Logger, soloReceiver bool) {
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		// update system metrics each call
		metricsData, err := GetMetricsDataFromContext(parentCtx)
		if err != nil {
			http.Error(w, "failed to get metrics data", http.StatusInternalServerError)
			return
		}
		UpdateSystemMetrics(parentCtx, logger, metricsData)
		//
		if r.URL.Query().Get("format") == "json" {
			s.handleJsonMetrics(parentCtx, soloReceiver)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handlePrometheusMetrics(parentCtx, soloReceiver)(w, r)
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

// handleMetrics processes the /metrics endpoint
func (s *Server) handleJsonMetrics(ctx context.Context, soloReceiver bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
		w.Header().Set("Content-Type", "application/json")
		// json.NewEncoder(w).Encode(metricsData)
		customEncoder := json.NewEncoder(w)
		customEncoder.SetEscapeHTML(false)
		if soloReceiver {
			customEncoder.Encode(struct {
				HostCPUCount              int                      `json:"host_cpu_count"`
				HostCPUUsedCount          int                      `json:"host_cpu_used_count"`
				HostCPUUsagePercentage    float64                  `json:"host_cpu_usage_percentage"`
				HostMemoryTotalBytes      uint64                   `json:"host_memory_total_bytes"`
				HostMemoryUsedBytes       uint64                   `json:"host_memory_used_bytes"`
				HostMemoryAvailableBytes  uint64                   `json:"host_memory_available_bytes"`
				HostMemoryUsagePercentage float64                  `json:"host_memory_usage_percentage"`
				HostDiskTotalBytes        uint64                   `json:"host_disk_total_bytes"`
				HostDiskUsedBytes         uint64                   `json:"host_disk_used_bytes"`
				HostDiskAvailableBytes    uint64                   `json:"host_disk_available_bytes"`
				HostDiskUsagePercentage   float64                  `json:"host_disk_usage_percentage"`
				Plugins                   []map[string]interface{} `json:"plugins"`
			}{
				HostCPUCount:              metricsData.HostCPUCount,
				HostCPUUsedCount:          metricsData.HostCPUUsedCount,
				HostCPUUsagePercentage:    metricsData.HostCPUUsagePercentage,
				HostMemoryTotalBytes:      metricsData.HostMemoryTotalBytes,
				HostMemoryUsedBytes:       metricsData.HostMemoryUsedBytes,
				HostMemoryAvailableBytes:  metricsData.HostMemoryAvailableBytes,
				HostMemoryUsagePercentage: metricsData.HostMemoryUsagePercentage,
				HostDiskTotalBytes:        metricsData.HostDiskTotalBytes,
				HostDiskUsedBytes:         metricsData.HostDiskUsedBytes,
				HostDiskAvailableBytes:    metricsData.HostDiskAvailableBytes,
				HostDiskUsagePercentage:   metricsData.HostDiskUsagePercentage,
				Plugins: func() []map[string]interface{} {
					plugins := make([]map[string]interface{}, len(metricsData.Plugins))
					for i, plugin := range metricsData.Plugins {
						pluginMap := make(map[string]interface{})
						switch s := plugin.(type) {
						case Plugin:
							pluginMap["name"] = s.Name
							pluginMap["plugin_name"] = s.PluginName
							if s.RepoName != "" {
								pluginMap["repo_name"] = s.RepoName
							}
							pluginMap["owner_name"] = s.OwnerName
							pluginMap["status"] = s.Status
							pluginMap["status_since"] = s.StatusSince
							pluginMap["last_successful_run_job_url"] = s.LastSuccessfulRunJobUrl
							pluginMap["last_failed_run_job_url"] = s.LastFailedRunJobUrl
							pluginMap["last_canceled_run_job_url"] = s.LastCanceledRunJobUrl
							pluginMap["last_successful_run"] = s.LastSuccessfulRun
							pluginMap["last_failed_run"] = s.LastFailedRun
							pluginMap["last_canceled_run"] = s.LastCanceledRun
						case PluginBase:
							pluginMap["name"] = s.Name
							pluginMap["plugin_name"] = s.PluginName
							if s.RepoName != "" {
								pluginMap["repo_name"] = s.RepoName
							}
							pluginMap["owner_name"] = s.OwnerName
							pluginMap["status"] = s.Status
							pluginMap["status_since"] = s.StatusSince
						}
						plugins[i] = pluginMap
					}
					return plugins
				}(),
			})
		} else {
			customEncoder.Encode(struct {
				TotalRunningVMs               int                      `json:"total_running_vms"`
				TotalSuccessfulRunsSinceStart int                      `json:"total_successful_runs_since_start"`
				TotalFailedRunsSinceStart     int                      `json:"total_failed_runs_since_start"`
				TotalCanceledRunsSinceStart   int                      `json:"total_canceled_runs_since_start"`
				HostCPUCount                  int                      `json:"host_cpu_count"`
				HostCPUUsedCount              int                      `json:"host_cpu_used_count"`
				HostCPUUsagePercentage        float64                  `json:"host_cpu_usage_percentage"`
				HostMemoryTotalBytes          uint64                   `json:"host_memory_total_bytes"`
				HostMemoryUsedBytes           uint64                   `json:"host_memory_used_bytes"`
				HostMemoryAvailableBytes      uint64                   `json:"host_memory_available_bytes"`
				HostMemoryUsagePercentage     float64                  `json:"host_memory_usage_percentage"`
				HostDiskTotalBytes            uint64                   `json:"host_disk_total_bytes"`
				HostDiskUsedBytes             uint64                   `json:"host_disk_used_bytes"`
				HostDiskAvailableBytes        uint64                   `json:"host_disk_available_bytes"`
				HostDiskUsagePercentage       float64                  `json:"host_disk_usage_percentage"`
				Plugins                       []map[string]interface{} `json:"plugins"`
			}{
				TotalRunningVMs:               metricsData.TotalRunningVMs,
				TotalSuccessfulRunsSinceStart: metricsData.TotalSuccessfulRunsSinceStart,
				TotalFailedRunsSinceStart:     metricsData.TotalFailedRunsSinceStart,
				TotalCanceledRunsSinceStart:   metricsData.TotalCanceledRunsSinceStart,
				HostCPUCount:                  metricsData.HostCPUCount,
				HostCPUUsedCount:              metricsData.HostCPUUsedCount,
				HostCPUUsagePercentage:        metricsData.HostCPUUsagePercentage,
				HostMemoryTotalBytes:          metricsData.HostMemoryTotalBytes,
				HostMemoryUsedBytes:           metricsData.HostMemoryUsedBytes,
				HostMemoryAvailableBytes:      metricsData.HostMemoryAvailableBytes,
				HostMemoryUsagePercentage:     metricsData.HostMemoryUsagePercentage,
				HostDiskTotalBytes:            metricsData.HostDiskTotalBytes,
				HostDiskUsedBytes:             metricsData.HostDiskUsedBytes,
				HostDiskAvailableBytes:        metricsData.HostDiskAvailableBytes,
				HostDiskUsagePercentage:       metricsData.HostDiskUsagePercentage,
				Plugins: func() []map[string]interface{} {
					plugins := make([]map[string]interface{}, len(metricsData.Plugins))
					for i, plugin := range metricsData.Plugins {
						pluginMap := make(map[string]interface{})
						switch s := plugin.(type) {
						case Plugin:
							pluginMap["name"] = s.Name
							pluginMap["plugin_name"] = s.PluginName
							if s.RepoName != "" {
								pluginMap["repo_name"] = s.RepoName
							}
							pluginMap["owner_name"] = s.OwnerName
							pluginMap["status"] = s.Status
							pluginMap["status_since"] = s.StatusSince
							pluginMap["last_successful_run_job_url"] = s.LastSuccessfulRunJobUrl
							pluginMap["last_failed_run_job_url"] = s.LastFailedRunJobUrl
							pluginMap["last_successful_run"] = s.LastSuccessfulRun
							pluginMap["last_failed_run"] = s.LastFailedRun
							pluginMap["last_canceled_run_job_url"] = s.LastCanceledRunJobUrl
							pluginMap["last_canceled_run"] = s.LastCanceledRun
						case PluginBase:
							pluginMap["name"] = s.Name
							pluginMap["plugin_name"] = s.PluginName
							if s.RepoName != "" {
								pluginMap["repo_name"] = s.RepoName
							}
							pluginMap["owner_name"] = s.OwnerName
							pluginMap["status"] = s.Status
							pluginMap["status_since"] = s.StatusSince
						}
						plugins[i] = pluginMap
					}
					return plugins
				}(),
			})
		}
	}
}

func (s *Server) handlePrometheusMetrics(ctx context.Context, soloReceiver bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
		w.Header().Set("Content-Type", "text/plain")
		if !soloReceiver {
			w.Write([]byte(fmt.Sprintf("total_running_vms %d\n", metricsData.TotalRunningVMs)))
			w.Write([]byte(fmt.Sprintf("total_successful_runs_since_start %d\n", metricsData.TotalSuccessfulRunsSinceStart)))
			w.Write([]byte(fmt.Sprintf("total_failed_runs_since_start %d\n", metricsData.TotalFailedRunsSinceStart)))
			w.Write([]byte(fmt.Sprintf("total_canceled_runs_since_start %d\n", metricsData.TotalCanceledRunsSinceStart)))
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
				w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,plugin=%s,owner=%s} %s\n", name, pluginName, ownerName, status)))
			} else {
				w.Write([]byte(fmt.Sprintf("plugin_status{name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, status)))
			}
			if !strings.Contains(pluginName, "_receiver") {
				if repoName == "" {
					w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))))
					w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))))
					w.Write([]byte(fmt.Sprintf("plugin_last_canceled_run{name=%s,plugin=%s,owner=%s,job_url=%s} %s\n", name, pluginName, ownerName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))))
				} else {
					w.Write([]byte(fmt.Sprintf("plugin_last_successful_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))))
					w.Write([]byte(fmt.Sprintf("plugin_last_failed_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))))
					w.Write([]byte(fmt.Sprintf("plugin_last_canceled_run{name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastCanceledRunJobUrl, lastCanceledRun.Format(time.RFC3339))))
				}
			}
			if repoName == "" {
				w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,plugin=%s,owner=%s} %s\n", name, pluginName, ownerName, StatusSince.Format(time.RFC3339))))
			} else {
				w.Write([]byte(fmt.Sprintf("plugin_status_since{name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, StatusSince.Format(time.RFC3339))))
			}
		}
		w.Write([]byte(fmt.Sprintf("host_cpu_count %d\n", metricsData.HostCPUCount)))
		w.Write([]byte(fmt.Sprintf("host_cpu_used_count %d\n", metricsData.HostCPUUsedCount)))
		w.Write([]byte(fmt.Sprintf("host_cpu_usage_percentage %f\n", metricsData.HostCPUUsagePercentage)))
		w.Write([]byte(fmt.Sprintf("host_memory_total_bytes %d\n", metricsData.HostMemoryTotalBytes)))
		w.Write([]byte(fmt.Sprintf("host_memory_used_bytes %d\n", metricsData.HostMemoryUsedBytes)))
		w.Write([]byte(fmt.Sprintf("host_memory_available_bytes %d\n", metricsData.HostMemoryAvailableBytes)))
		w.Write([]byte(fmt.Sprintf("host_memory_usage_percentage %f\n", metricsData.HostMemoryUsagePercentage)))
		w.Write([]byte(fmt.Sprintf("host_disk_total_bytes %d\n", metricsData.HostDiskTotalBytes)))
		w.Write([]byte(fmt.Sprintf("host_disk_used_bytes %d\n", metricsData.HostDiskUsedBytes)))
		w.Write([]byte(fmt.Sprintf("host_disk_available_bytes %d\n", metricsData.HostDiskAvailableBytes)))
		w.Write([]byte(fmt.Sprintf("host_disk_usage_percentage %f\n", metricsData.HostDiskUsagePercentage)))
	}
}

func GetMetricsDataFromContext(ctx context.Context) (*MetricsDataLock, error) {
	metricsData, ok := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
	if !ok {
		return nil, fmt.Errorf("GetMetricsDataFromContext failed")
	}
	return metricsData, nil
}
