package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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

type Service struct {
	Name                    string
	PluginName              string
	RepoName                string
	OwnerName               string
	Status                  string
	LastSuccessfulRunJobUrl string
	LastFailedRunJobUrl     string
	LastSuccessfulRun       time.Time
	LastFailedRun           time.Time
}

type MetricsData struct {
	sync.RWMutex
	TotalRunningVMs               int
	TotalSuccessfulRunsSinceStart int
	TotalFailedRunsSinceStart     int
	HostCPUCount                  int
	HostCPUUsedCount              int
	HostCPUUsagePercentage        float64
	HostMemoryTotal               uint64
	HostMemoryUsed                uint64
	HostMemoryAvailable           uint64
	HostMemoryUsagePercentage     float64
	HostDiskTotal                 uint64
	HostDiskUsed                  uint64
	HostDiskAvailable             uint64
	HostDiskUsagePercentage       float64
	Services                      []Service
}

func (m *MetricsData) AddService(service Service) {
	m.Lock()
	defer m.Unlock()
	found := false
	for i, svc := range m.Services {
		if svc.Name == service.Name {
			m.Services[i] = service
			found = true
			break
		}
	}
	if !found {
		m.Services = append(m.Services, service)
	}
}

func (m *MetricsData) IncrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	m.TotalRunningVMs++
}

func (m *MetricsData) DecrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	if m.TotalRunningVMs > 0 {
		m.TotalRunningVMs--
	}
}

func (m *MetricsData) IncrementTotalSuccessfulRunsSinceStart() {
	m.Lock()
	defer m.Unlock()
	m.TotalSuccessfulRunsSinceStart++
}

func (m *MetricsData) IncrementTotalFailedRunsSinceStart() {
	m.Lock()
	defer m.Unlock()
	m.TotalFailedRunsSinceStart++
}

func CompareAndUpdateMetrics(currentService Service, updatedService Service) Service {
	if updatedService.PluginName != "" {
		currentService.PluginName = updatedService.PluginName
	}
	if updatedService.Status != "" {
		currentService.Status = updatedService.Status
	}
	if !updatedService.LastSuccessfulRun.IsZero() {
		currentService.LastSuccessfulRun = updatedService.LastSuccessfulRun
	}
	if !updatedService.LastFailedRun.IsZero() {
		currentService.LastFailedRun = updatedService.LastFailedRun
	}
	if updatedService.LastSuccessfulRunJobUrl != "" {
		currentService.LastSuccessfulRunJobUrl = updatedService.LastSuccessfulRunJobUrl
	}
	if updatedService.LastFailedRunJobUrl != "" {
		currentService.LastFailedRunJobUrl = updatedService.LastFailedRunJobUrl
	}
	return currentService
}

func UpdateSystemMetrics(serviceCtx context.Context, logger *slog.Logger, metricsData *MetricsData) {
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
		metricsData.HostMemoryTotal = 0
	}
	metricsData.HostMemoryTotal = uint64(memStat.Total)
	metricsData.HostMemoryAvailable = uint64(memStat.Available)
	metricsData.HostMemoryUsagePercentage = memStat.UsedPercent
	metricsData.HostMemoryUsed = uint64(memStat.Used)
	// DISK
	diskStat, err := disk.Usage("/")
	if err != nil {
		logger.ErrorContext(serviceCtx, "Error getting disk usage", "error", err)
		metricsData.HostDiskUsagePercentage = 0
	}
	metricsData.HostDiskUsagePercentage = diskStat.UsedPercent
	metricsData.HostDiskTotal = uint64(diskStat.Total)
	metricsData.HostDiskAvailable = uint64(diskStat.Free)
	metricsData.HostDiskUsed = uint64(diskStat.Used)
}

func UpdateService(workerCtx context.Context, serviceCtx context.Context, logger *slog.Logger, updatedService Service) {
	service := config.GetServiceFromContext(serviceCtx)
	metricsData := GetMetricsDataFromContext(workerCtx)
	for i, svc := range metricsData.Services {
		if svc.Name == service.Name {
			newService := Service{
				Name:                    service.Name,
				RepoName:                service.Repo,
				OwnerName:               service.Owner,
				PluginName:              metricsData.Services[i].PluginName,
				Status:                  metricsData.Services[i].Status,
				LastSuccessfulRun:       metricsData.Services[i].LastSuccessfulRun,
				LastFailedRun:           metricsData.Services[i].LastFailedRun,
				LastSuccessfulRunJobUrl: metricsData.Services[i].LastSuccessfulRunJobUrl,
				LastFailedRunJobUrl:     metricsData.Services[i].LastFailedRunJobUrl,
			}
			newService = CompareAndUpdateMetrics(newService, updatedService)
			metricsData.Services[i] = newService
		}
	}
	UpdateSystemMetrics(serviceCtx, logger, metricsData)
}

func (m *MetricsData) UpdateService(serviceCtx context.Context, logger *slog.Logger, updatedService Service) {
	m.Lock()
	defer m.Unlock()
	if updatedService.Name == "" {
		panic("updateService.Name is required")
	}
	for i, svc := range m.Services {
		if svc.Name == updatedService.Name {
			m.Services[i] = CompareAndUpdateMetrics(svc, updatedService)
			UpdateSystemMetrics(serviceCtx, logger, m)
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
func (s *Server) Start(parentCtx context.Context) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "json" {
			s.handleJsonMetrics(parentCtx)(w, r)
		} else if r.URL.Query().Get("format") == "prometheus" {
			s.handlePrometheusMetrics(parentCtx)(w, r)
		} else {
			http.Error(w, "Unsupported format", http.StatusBadRequest)
		}
	})
	http.ListenAndServe(":"+s.Port, nil)
}

// handleMetrics processes the /metrics endpoint
func (s *Server) handleJsonMetrics(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsData)
		// services := metricsData.GetServices()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metricsData)
	}
}

func (s *Server) handlePrometheusMetrics(ctx context.Context) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metricsData := ctx.Value(config.ContextKey("metrics")).(*MetricsData)
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(fmt.Sprintf("total_running_vms %d\n", metricsData.TotalRunningVMs)))
		w.Write([]byte(fmt.Sprintf("total_successful_runs_since_start %d\n", metricsData.TotalSuccessfulRunsSinceStart)))
		w.Write([]byte(fmt.Sprintf("total_failed_runs_since_start %d\n", metricsData.TotalFailedRunsSinceStart)))
		for _, service := range metricsData.Services {
			w.Write([]byte(fmt.Sprintf("service_status{service_name=%s,plugin=%s,owner=%s,repo=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, service.Status)))
			w.Write([]byte(fmt.Sprintf("service_last_successful_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, service.LastSuccessfulRunJobUrl, service.LastSuccessfulRun.Format(time.RFC3339))))
			w.Write([]byte(fmt.Sprintf("service_last_failed_run{service_name=%s,plugin=%s,owner=%s,repo=%s,job_url=%s} %s\n", service.Name, service.PluginName, service.OwnerName, service.RepoName, service.LastFailedRunJobUrl, service.LastFailedRun.Format(time.RFC3339))))
		}
		w.Write([]byte(fmt.Sprintf("host_cpu_count %d\n", metricsData.HostCPUCount)))
		w.Write([]byte(fmt.Sprintf("host_cpu_used_count %d\n", metricsData.HostCPUUsedCount)))
		w.Write([]byte(fmt.Sprintf("host_cpu_usage_percentage %f\n", metricsData.HostCPUUsagePercentage)))
		w.Write([]byte(fmt.Sprintf("host_memory_total %d\n", metricsData.HostMemoryTotal)))
		w.Write([]byte(fmt.Sprintf("host_memory_used %d\n", metricsData.HostMemoryUsed)))
		w.Write([]byte(fmt.Sprintf("host_memory_available %d\n", metricsData.HostMemoryAvailable)))
		w.Write([]byte(fmt.Sprintf("host_memory_usage_percentage %f\n", metricsData.HostMemoryUsagePercentage)))
		w.Write([]byte(fmt.Sprintf("host_disk_total %d\n", metricsData.HostDiskTotal)))
		w.Write([]byte(fmt.Sprintf("host_disk_used %d\n", metricsData.HostDiskUsed)))
		w.Write([]byte(fmt.Sprintf("host_disk_available %d\n", metricsData.HostDiskAvailable)))
		w.Write([]byte(fmt.Sprintf("host_disk_usage_percentage %f\n", metricsData.HostDiskUsagePercentage)))
	}
}

func GetMetricsDataFromContext(ctx context.Context) *MetricsData {
	metricsData, ok := ctx.Value(config.ContextKey("metrics")).(*MetricsData)
	if !ok {
		panic("GetHttpTransportFromContext failed")
	}
	return metricsData
}
