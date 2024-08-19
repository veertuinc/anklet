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

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/veertuinc/anklet/internal/config"
)

// Server defines the structure for the API server
type Server struct {
	Port string
}

type ServiceBase struct {
	Name               string
	PluginName         string
	RepoName           string
	OwnerName          string
	Status             string
	StatusRunningSince time.Time
}

type Service struct {
	*ServiceBase
	LastSuccessfulRunJobUrl string
	LastFailedRunJobUrl     string
	LastSuccessfulRun       time.Time
	LastFailedRun           time.Time
}

type MetricsData struct {
	TotalRunningVMs               int
	TotalSuccessfulRunsSinceStart int
	TotalFailedRunsSinceStart     int
	HostCPUCount                  int
	HostCPUUsedCount              int
	HostCPUUsagePercentage        float64
	HostMemoryTotalBytes          uint64
	HostMemoryUsedBytes           uint64
	HostMemoryAvailableBytes      uint64
	HostMemoryUsagePercentage     float64
	HostDiskTotalBytes            uint64
	HostDiskUsedBytes             uint64
	HostDiskAvailableBytes        uint64
	HostDiskUsagePercentage       float64
	Services                      []interface{}
}

type MetricsDataLock struct {
	sync.RWMutex
	MetricsData
}

func (m *MetricsDataLock) AddService(service ServiceBase) {
	m.Lock()
	defer m.Unlock()
	found := false
	for i, svc := range m.Services {
		var name string
		svc, ok := svc.(ServiceBase)
		if !ok {
			panic("unable to get service name")
		}
		name = svc.Name
		if name == service.Name {
			m.Services[i] = service
			found = true
			break
		}
	}
	if !found {
		m.Services = append(m.Services, service)
	}
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

func CompareAndUpdateMetrics(currentService interface{}, updatedService interface{}) interface{} {
	switch currentServiceTyped := currentService.(type) {
	case Service:
		updated, ok := updatedService.(Service)
		if !ok {
			panic("unable to convert updatedService to Service")
		}
		if updated.PluginName != "" {
			currentServiceTyped.PluginName = updated.PluginName
		}
		if updated.Status != "" {
			if currentServiceTyped.Status != updated.Status {
				currentServiceTyped.StatusRunningSince = time.Now()
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
		return currentServiceTyped
	case ServiceBase:
		updated, ok := updatedService.(ServiceBase)
		if !ok {
			panic("unable to convert updatedService to ServiceBase")
		}
		if updated.PluginName != "" {
			currentServiceTyped.PluginName = updated.PluginName
		}
		if updated.Status != "" {
			if currentServiceTyped.Status != updated.Status {
				currentServiceTyped.StatusRunningSince = time.Now()
			}
			currentServiceTyped.Status = updated.Status
		}
		return currentServiceTyped
	default:
		panic("unable to convert currentService to Service or ServiceBase")
	}
}

func UpdateSystemMetrics(serviceCtx context.Context, logger *slog.Logger, metricsData *MetricsDataLock) {
	cpuCount, err := cpu.Counts(false)
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting CPU count", "error", err)
		metricsData.HostCPUCount = 0
	}
	metricsData.HostCPUCount = cpuCount
	cpuUsedPercent, err := cpu.Percent(0, false)
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting CPU usage", "error", err)
		metricsData.HostCPUUsagePercentage = 0
	}
	metricsData.HostCPUUsagePercentage = cpuUsedPercent[0]
	metricsData.HostCPUUsedCount = int(float64(cpuCount) * (metricsData.HostCPUUsagePercentage / 100))
	// MEMORY
	memStat, err := mem.VirtualMemory()
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting memory usage", "error", err)
		metricsData.HostMemoryTotalBytes = 0
	}
	metricsData.HostMemoryTotalBytes = uint64(memStat.Total)
	metricsData.HostMemoryAvailableBytes = uint64(memStat.Available)
	metricsData.HostMemoryUsagePercentage = memStat.UsedPercent
	metricsData.HostMemoryUsedBytes = uint64(memStat.Used)
	// DISK
	diskStat, err := disk.Usage("/")
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting disk usage", "error", err)
		metricsData.HostDiskUsagePercentage = 0
	}
	metricsData.HostDiskUsagePercentage = diskStat.UsedPercent
	metricsData.HostDiskTotalBytes = uint64(diskStat.Total)
	metricsData.HostDiskAvailableBytes = uint64(diskStat.Free)
	metricsData.HostDiskUsedBytes = uint64(diskStat.Used)
}

func UpdateService(workerCtx context.Context, serviceCtx context.Context, logger *slog.Logger, updatedService interface{}) {
	service := config.GetServiceFromContext(serviceCtx)
	metricsData := GetMetricsDataFromContext(workerCtx)
	for i, currentService := range metricsData.Services {
		switch fullUpdatedService := updatedService.(type) {
		case Service:
			if fullUpdatedService.Name == service.Name {
				newService := CompareAndUpdateMetrics(currentService, updatedService)
				metricsData.Services[i] = newService
			}
		case ServiceBase:
			newService := CompareAndUpdateMetrics(currentService, updatedService)
			metricsData.Services[i] = newService
		default:
			panic("unable to convert svc to Service or ServiceBase")
		}
	}
}

func (m *MetricsDataLock) UpdateService(serviceCtx context.Context, logger *slog.Logger, updatedService interface{}) {
	m.Lock()
	defer m.Unlock()
	var name string
	switch fullUpdatedService := updatedService.(type) {
	case Service:
		if fullUpdatedService.Name == "" {
			panic("updateService.Name is required")
		}
		for i, svc := range m.Services {
			switch typedSvc := svc.(type) {
			case Service:
				if fullUpdatedService.Name == typedSvc.Name {
					m.Services[i] = CompareAndUpdateMetrics(typedSvc, updatedService)
				}
			}
		}
	case ServiceBase:
		name = fullUpdatedService.Name
		for i, svc := range m.Services {
			switch typedSvc := svc.(type) {
			case ServiceBase:
				if name == typedSvc.Name {
					m.Services[i] = CompareAndUpdateMetrics(typedSvc, updatedService)
				}
			}
		}
	}
}

// NewServer creates a new instance of Server
func NewServer(port string) *Server {
	return &Server{
		Port: port,
	}
}

// Start runs the HTTP server
func (s *Server) Start(parentCtx context.Context, logger *slog.Logger) {
	http.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		// update system metrics each call
		metricsData := GetMetricsDataFromContext(parentCtx)
		UpdateSystemMetrics(parentCtx, logger, metricsData)
		//
		if r.URL.Query().Get("format") == "json" {
			s.handleJsonMetrics(parentCtx)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handlePrometheusMetrics(parentCtx)(w, r)
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
func (s *Server) handleJsonMetrics(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metricsData)
	}
}

func (s *Server) handlePrometheusMetrics(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(fmt.Sprintf("total_running_vms %d\n", metricsData.TotalRunningVMs)))
		w.Write([]byte(fmt.Sprintf("total_successful_runs_since_start %d\n", metricsData.TotalSuccessfulRunsSinceStart)))
		w.Write([]byte(fmt.Sprintf("total_failed_runs_since_start %d\n", metricsData.TotalFailedRunsSinceStart)))
		for _, service := range metricsData.Services {
			var name string
			var pluginName string
			var repoName string
			var ownerName string
			var status string
			var statusRunningSince time.Time
			var lastSuccessfulRun time.Time
			var lastFailedRun time.Time
			var lastSuccessfulRunJobUrl string
			var lastFailedRunJobUrl string
			switch svc := service.(type) {
			case Service:
				name = svc.Name
				pluginName = svc.PluginName
				repoName = svc.RepoName
				ownerName = svc.OwnerName
				status = svc.Status
				statusRunningSince = svc.StatusRunningSince
				lastSuccessfulRun = svc.LastSuccessfulRun
				lastFailedRun = svc.LastFailedRun
				lastSuccessfulRunJobUrl = svc.LastSuccessfulRunJobUrl
				lastFailedRunJobUrl = svc.LastFailedRunJobUrl
			case ServiceBase:
				name = svc.Name
				pluginName = svc.PluginName
				repoName = svc.RepoName
				ownerName = svc.OwnerName
				status = svc.Status
				statusRunningSince = svc.StatusRunningSince
			default:
				panic("unable to convert svc to Service or ServiceBase")
			}
			w.Write([]byte(fmt.Sprintf("service_status{service_name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, status)))
			if !strings.Contains(pluginName, "_controller") {
				w.Write([]byte(fmt.Sprintf("service_last_successful_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastSuccessfulRunJobUrl, lastSuccessfulRun.Format(time.RFC3339))))
				w.Write([]byte(fmt.Sprintf("service_last_failed_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", name, pluginName, ownerName, repoName, lastFailedRunJobUrl, lastFailedRun.Format(time.RFC3339))))
				w.Write([]byte(fmt.Sprintf("service_status_running_since{service_name=%s,plugin=%s,owner=%s,repo=%s} %s\n", name, pluginName, ownerName, repoName, statusRunningSince.Format(time.RFC3339))))
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

func GetMetricsDataFromContext(ctx context.Context) *MetricsDataLock {
	metricsData, ok := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
	if !ok {
		panic("GetHttpTransportFromContext failed")
	}
	return metricsData
}
