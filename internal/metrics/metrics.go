package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpWriteTimeout      = 30 * time.Second
	httpIdleTimeout       = 60 * time.Second
)

// Server serves local or aggregated metrics over HTTP.
type Server struct {
	BindAddress string
	Port        string
}

type PluginBase struct {
	Name        string
	PluginName  string
	RepoName    string
	OwnerName   string
	IsReceiver  bool
	Status      string
	StatusSince time.Time
}

type Plugin struct {
	PluginBase
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

func (p Plugin) isReceiverPlugin() bool {
	return p.IsReceiver || strings.HasSuffix(p.PluginName, "_receiver")
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
	Plugins                       []Plugin  `json:"plugins"`
}

type MetricsDataLock struct {
	sync.RWMutex
	MetricsData
}

func (m *MetricsDataLock) findPluginIndexLocked(name string) int {
	for i := range m.Plugins {
		if m.Plugins[i].Name == name {
			return i
		}
	}
	return -1
}

func (m *MetricsDataLock) AddPlugin(plugin Plugin) error {
	if plugin.Name == "" {
		return fmt.Errorf("plugin name is required")
	}
	m.Lock()
	defer m.Unlock()
	if m.findPluginIndexLocked(plugin.Name) >= 0 {
		return nil
	}
	m.Plugins = append(m.Plugins, plugin)
	return nil
}

func (m *MetricsDataLock) IncrementTotalRunningVMs(
	_ context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	m.Lock()
	defer m.Unlock()
	m.TotalRunningVMs++
	if err == nil {
		if idx := m.findPluginIndexLocked(pluginConfig.Name); idx >= 0 {
			m.Plugins[idx].TotalRanVMs++
		}
	}
}

func (m *MetricsDataLock) DecrementTotalRunningVMs() {
	m.Lock()
	defer m.Unlock()
	if m.TotalRunningVMs > 0 {
		m.TotalRunningVMs--
	}
}

func (m *MetricsDataLock) IncrementTotalSuccessfulRunsSinceStart(
	_ context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	m.Lock()
	defer m.Unlock()
	m.TotalSuccessfulRunsSinceStart++
	if err == nil {
		if idx := m.findPluginIndexLocked(pluginConfig.Name); idx >= 0 {
			m.Plugins[idx].TotalSuccessfulRunsSinceStart++
		}
	}
}

func (m *MetricsDataLock) IncrementTotalFailedRunsSinceStart(
	_ context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	m.Lock()
	defer m.Unlock()
	m.TotalFailedRunsSinceStart++
	if err == nil {
		if idx := m.findPluginIndexLocked(pluginConfig.Name); idx >= 0 {
			m.Plugins[idx].TotalFailedRunsSinceStart++
		}
	}
}

func (m *MetricsDataLock) IncrementTotalCanceledRunsSinceStart(
	_ context.Context,
	pluginCtx context.Context,
) {
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	m.Lock()
	defer m.Unlock()
	m.TotalCanceledRunsSinceStart++
	if err == nil {
		if idx := m.findPluginIndexLocked(pluginConfig.Name); idx >= 0 {
			m.Plugins[idx].TotalCanceledRunsSinceStart++
		}
	}
}

func mergePluginMetrics(current Plugin, updated Plugin) Plugin {
	if updated.PluginName != "" {
		current.PluginName = updated.PluginName
	}
	if updated.IsReceiver {
		current.IsReceiver = true
	}
	if updated.Status != "" {
		if current.Status != updated.Status {
			current.StatusSince = time.Now()
		}
		current.Status = updated.Status
	}
	if !updated.LastSuccessfulRun.IsZero() {
		current.LastSuccessfulRun = updated.LastSuccessfulRun
	}
	if !updated.LastFailedRun.IsZero() {
		current.LastFailedRun = updated.LastFailedRun
	}
	if !updated.LastCanceledRun.IsZero() {
		current.LastCanceledRun = updated.LastCanceledRun
	}
	if updated.LastSuccessfulRunJobUrl != "" {
		current.LastSuccessfulRunJobUrl = updated.LastSuccessfulRunJobUrl
	}
	if updated.LastFailedRunJobUrl != "" {
		current.LastFailedRunJobUrl = updated.LastFailedRunJobUrl
	}
	if updated.LastCanceledRunJobUrl != "" {
		current.LastCanceledRunJobUrl = updated.LastCanceledRunJobUrl
	}
	if updated.TotalCanceledRunsSinceStart > current.TotalCanceledRunsSinceStart {
		current.TotalCanceledRunsSinceStart = updated.TotalCanceledRunsSinceStart
	}
	if updated.TotalFailedRunsSinceStart > current.TotalFailedRunsSinceStart {
		current.TotalFailedRunsSinceStart = updated.TotalFailedRunsSinceStart
	}
	if updated.TotalSuccessfulRunsSinceStart > current.TotalSuccessfulRunsSinceStart {
		current.TotalSuccessfulRunsSinceStart = updated.TotalSuccessfulRunsSinceStart
	}
	if updated.TotalRanVMs > current.TotalRanVMs {
		current.TotalRanVMs = updated.TotalRanVMs
	}
	return current
}

func (m *MetricsDataLock) UpdatePlugin(
	_ context.Context,
	pluginCtx context.Context,
	updatedPlugin Plugin,
) error {
	name := updatedPlugin.Name
	if name == "" {
		if ctxPlugin, err := config.GetPluginFromContext(pluginCtx); err == nil {
			name = ctxPlugin.Name
		}
	}
	if name == "" {
		return fmt.Errorf("updatePlugin.Name is required")
	}
	m.Lock()
	defer m.Unlock()
	if idx := m.findPluginIndexLocked(name); idx >= 0 {
		m.Plugins[idx] = mergePluginMetrics(m.Plugins[idx], updatedPlugin)
	}
	return nil
}

func (m *MetricsDataLock) SetStatus(pluginCtx context.Context, status string) error {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}
	return m.SetStatusByName(ctxPlugin.Name, status)
}

func (m *MetricsDataLock) SetStatusByName(pluginName string, status string) error {
	if pluginName == "" {
		return fmt.Errorf("plugin name is required")
	}
	m.Lock()
	defer m.Unlock()
	if idx := m.findPluginIndexLocked(pluginName); idx >= 0 {
		if m.Plugins[idx].Status != status {
			m.Plugins[idx].Status = status
			m.Plugins[idx].StatusSince = time.Now()
		}
	}
	return nil
}

// Snapshot returns a race-free deep copy of MetricsData.
func (m *MetricsDataLock) Snapshot() MetricsData {
	m.RLock()
	defer m.RUnlock()
	snap := m.MetricsData
	if m.Plugins != nil {
		snap.Plugins = make([]Plugin, len(m.Plugins))
		copy(snap.Plugins, m.Plugins)
	}
	return snap
}

func UpdateSystemMetrics(pluginCtx context.Context, metricsData *MetricsDataLock) {
	cpuCount, cpuCountErr := cpu.Counts(false)
	if cpuCountErr != nil {
		logging.Error(pluginCtx, "Error getting CPU count", "error", cpuCountErr)
		cpuCount = 0
	}

	cpuUsedPercentSlice, cpuPctErr := cpu.Percent(0, false)
	var cpuUsagePct float64
	var cpuUsedCount int
	if cpuPctErr != nil || len(cpuUsedPercentSlice) == 0 {
		if cpuPctErr != nil {
			logging.Error(pluginCtx, "Error getting CPU usage", "error", cpuPctErr)
		}
	} else {
		cpuUsagePct = cpuUsedPercentSlice[0]
		cpuUsedCount = int(float64(cpuCount) * (cpuUsagePct / 100))
	}

	memStat, memErr := mem.VirtualMemory()
	if memErr != nil {
		logging.Error(pluginCtx, "Error getting memory usage", "error", memErr)
	}

	diskStat, diskErr := disk.Usage("/")
	if diskErr != nil {
		logging.Error(pluginCtx, "Error getting disk usage", "error", diskErr)
	}

	metricsData.Lock()
	defer metricsData.Unlock()

	metricsData.HostCPUCount = cpuCount
	metricsData.HostCPUUsagePercentage = cpuUsagePct
	metricsData.HostCPUUsedCount = cpuUsedCount

	if memErr == nil && memStat != nil {
		metricsData.HostMemoryTotalBytes = memStat.Total
		metricsData.HostMemoryAvailableBytes = memStat.Available
		metricsData.HostMemoryUsagePercentage = memStat.UsedPercent
		metricsData.HostMemoryUsedBytes = memStat.Used
	} else {
		metricsData.HostMemoryTotalBytes = 0
		metricsData.HostMemoryAvailableBytes = 0
		metricsData.HostMemoryUsagePercentage = 0
		metricsData.HostMemoryUsedBytes = 0
	}

	if diskErr == nil && diskStat != nil {
		metricsData.HostDiskUsagePercentage = diskStat.UsedPercent
		metricsData.HostDiskTotalBytes = diskStat.Total
		metricsData.HostDiskAvailableBytes = diskStat.Free
		metricsData.HostDiskUsedBytes = diskStat.Used
	} else {
		metricsData.HostDiskUsagePercentage = 0
		metricsData.HostDiskTotalBytes = 0
		metricsData.HostDiskAvailableBytes = 0
		metricsData.HostDiskUsedBytes = 0
	}
}

// NewServer creates a Server bound to all interfaces on port.
func NewServer(port string) *Server {
	return &Server{
		Port: port,
	}
}

// NewServerWithAddress creates a Server bound to bindAddress:port.
func NewServerWithAddress(bindAddress, port string) *Server {
	return &Server{
		BindAddress: bindAddress,
		Port:        port,
	}
}

func (s *Server) listenAddr() string {
	return net.JoinHostPort(s.BindAddress, s.Port)
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}
}

// Handler builds an isolated http.Handler for the local metrics server.
func (s *Server) Handler(parentCtx context.Context, soloReceiver bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics/v1", func(w http.ResponseWriter, r *http.Request) {
		metricsData, err := GetMetricsDataFromContext(parentCtx)
		if err != nil {
			http.Error(w, "failed to get metrics data", http.StatusInternalServerError)
			return
		}
		UpdateSystemMetrics(parentCtx, metricsData)
		if r.URL.Query().Get("format") == "prometheus" {
			s.handlePrometheusMetrics(parentCtx, metricsData, soloReceiver)(w, r)
		} else {
			http.Error(w, "unsupported format, please use '?format=prometheus'", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		if _, err := w.Write([]byte("please use /metrics/v1")); err != nil {
			logging.Debug(parentCtx, "error writing /metrics response", "error", err)
		}
	})
	return mux
}

// Start runs the HTTP metrics server by opening a listener on the configured address.
func (s *Server) Start(parentCtx context.Context, soloReceiver bool) {
	ln, err := net.Listen("tcp", s.listenAddr())
	if err != nil {
		logging.Error(parentCtx, "error listening for metrics server", "addr", s.listenAddr(), "error", err)
		return
	}
	s.StartWithListener(parentCtx, ln, soloReceiver)
}

// StartWithListener serves the local metrics endpoints on an already-bound listener.
func (s *Server) StartWithListener(parentCtx context.Context, ln net.Listener, soloReceiver bool) {
	server := newHTTPServer(ln.Addr().String(), s.Handler(parentCtx, soloReceiver))

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			logging.Error(parentCtx, "error starting metrics server", "error", err)
		}
	}()

	<-parentCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logging.Error(parentCtx, "error shutting down metrics server", "error", err)
	}
}

var labelSanitizer = strings.NewReplacer("\r", "", "\n", "", "}", "", ",", "")

func sanitizeLabel(value string) string {
	return labelSanitizer.Replace(value)
}

func writePrometheusLine(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func (s *Server) handlePrometheusMetrics(
	ctx context.Context,
	metricsData *MetricsDataLock,
	soloReceiver bool,
) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		snap := metricsData.Snapshot()
		w.Header().Set("Content-Type", "text/plain")

		if !soloReceiver {
			if err := writePrometheusLine(w, "total_running_vms %d\n", snap.TotalRunningVMs); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
			if err := writePrometheusLine(w, "total_successful_runs_since_start %d\n", snap.TotalSuccessfulRunsSinceStart); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
			if err := writePrometheusLine(w, "total_failed_runs_since_start %d\n", snap.TotalFailedRunsSinceStart); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
			if err := writePrometheusLine(w, "total_canceled_runs_since_start %d\n", snap.TotalCanceledRunsSinceStart); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
		}

		for _, plugin := range snap.Plugins {
			name := sanitizeLabel(plugin.Name)
			pluginName := sanitizeLabel(plugin.PluginName)
			ownerName := sanitizeLabel(plugin.OwnerName)
			repoName := sanitizeLabel(plugin.RepoName)
			status := sanitizeLabel(plugin.Status)
			baseLabels := fmt.Sprintf("name=%s,plugin=%s,owner=%s", name, pluginName, ownerName)
			if repoName != "" {
				baseLabels += ",repo=" + repoName
			}

			if err := writePrometheusLine(w, "plugin_status{%s} %s\n", baseLabels, status); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
			if !plugin.isReceiverPlugin() {
				if err := writePrometheusLine(w, "plugin_last_successful_run{%s,job_url=%s} %s\n",
					baseLabels, sanitizeLabel(plugin.LastSuccessfulRunJobUrl), plugin.LastSuccessfulRun.Format(time.RFC3339)); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
				if err := writePrometheusLine(w, "plugin_last_failed_run{%s,job_url=%s} %s\n",
					baseLabels, sanitizeLabel(plugin.LastFailedRunJobUrl), plugin.LastFailedRun.Format(time.RFC3339)); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
				if err := writePrometheusLine(w, "plugin_last_canceled_run{%s,job_url=%s} %s\n",
					baseLabels, sanitizeLabel(plugin.LastCanceledRunJobUrl), plugin.LastCanceledRun.Format(time.RFC3339)); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
			}
			if err := writePrometheusLine(w, "plugin_status_since{%s} %s\n", baseLabels, plugin.StatusSince.Format(time.RFC3339)); err != nil {
				logging.Debug(ctx, "error writing prometheus metrics", "error", err)
				return
			}
			if !plugin.isReceiverPlugin() {
				counterLabels := fmt.Sprintf("name=%s,plugin=%s,owner=%s", name, pluginName, ownerName)
				if err := writePrometheusLine(w, "plugin_total_ran_vms{%s} %d\n", counterLabels, plugin.TotalRanVMs); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
				if err := writePrometheusLine(w, "plugin_total_successful_runs_since_start{%s} %d\n", counterLabels, plugin.TotalSuccessfulRunsSinceStart); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
				if err := writePrometheusLine(w, "plugin_total_failed_runs_since_start{%s} %d\n", counterLabels, plugin.TotalFailedRunsSinceStart); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
				if err := writePrometheusLine(w, "plugin_total_canceled_runs_since_start{%s} %d\n", counterLabels, plugin.TotalCanceledRunsSinceStart); err != nil {
					logging.Debug(ctx, "error writing prometheus metrics", "error", err)
					return
				}
			}
		}

		hostLines := []struct {
			format string
			val    any
		}{
			{"host_cpu_count %d\n", snap.HostCPUCount},
			{"host_cpu_used_count %d\n", snap.HostCPUUsedCount},
			{"host_cpu_usage_percentage %f\n", snap.HostCPUUsagePercentage},
			{"host_memory_total_bytes %d\n", snap.HostMemoryTotalBytes},
			{"host_memory_used_bytes %d\n", snap.HostMemoryUsedBytes},
			{"host_memory_available_bytes %d\n", snap.HostMemoryAvailableBytes},
			{"host_memory_usage_percentage %f\n", snap.HostMemoryUsagePercentage},
			{"host_disk_total_bytes %d\n", snap.HostDiskTotalBytes},
			{"host_disk_used_bytes %d\n", snap.HostDiskUsedBytes},
			{"host_disk_available_bytes %d\n", snap.HostDiskAvailableBytes},
			{"host_disk_usage_percentage %f\n", snap.HostDiskUsagePercentage},
		}
		for _, line := range hostLines {
			if err := writePrometheusLine(w, line.format, line.val); err != nil {
				logging.Debug(ctx, "error writing prometheus host metrics", "error", err)
				return
			}
		}
	}
}

func GetMetricsDataFromContext(ctx context.Context) (*MetricsDataLock, error) {
	metricsData, ok := ctx.Value(config.ContextKey("metrics")).(*MetricsDataLock)
	if !ok || metricsData == nil {
		return nil, fmt.Errorf("GetMetricsDataFromContext failed")
	}
	return metricsData, nil
}

func Cleanup(ctx context.Context, owner string, name string) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		logging.Error(ctx, "error getting database client from context", "error", err.Error())
		return
	}

	cleanupKey := redisMetricsKey(owner, name)
	_, result := databaseContainer.RetryDel(ctx, cleanupKey)
	if result != nil {
		logging.Dev(ctx, "skipped deleting metrics data from Redis during cleanup (database unavailable)", "key", cleanupKey, "error", result.Error())
		return
	}
	logging.Dev(ctx, "successfully deleted metrics data from Redis", "key", cleanupKey)
}

// redisMetricsKey builds anklet/metrics/{owner}/{name}, omitting an empty owner segment
// so enterprise-scoped plugins (owner unset) do not produce a double slash.
func redisMetricsKey(owner, name string) string {
	if owner == "" {
		return "anklet/metrics/" + name
	}
	return "anklet/metrics/" + owner + "/" + name
}

func ExportMetricsToDB(workerCtx context.Context, pluginCtx context.Context, keyEnding string) {
	logging.Info(pluginCtx, "starting ExportMetricsToDB", "keyEnding", keyEnding)
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting database client from context", "error", err)
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	const maxConsecutiveErrors = 60
	consecutiveErrors := 0
	var atLeastOneRun atomic.Bool
	metricsKey := "anklet/metrics/" + strings.TrimPrefix(keyEnding, "/")

	go func() {
		defer ticker.Stop()
		for {
			if workerCtx.Err() != nil || pluginCtx.Err() != nil {
				return
			}
			metricsData, err := GetMetricsDataFromContext(workerCtx)
			if err != nil {
				logging.Error(pluginCtx, "error getting metrics data from context", "error", err.Error())
				select {
				case <-pluginCtx.Done():
					return
				case <-workerCtx.Done():
					return
				case <-ticker.C:
				}
				continue
			}

			UpdateSystemMetrics(pluginCtx, metricsData)
			snap := metricsData.Snapshot()
			snap.LastUpdate = time.Now()

			metricsDataJson, err := json.Marshal(snap)
			if err != nil {
				logging.Error(pluginCtx, "error marshalling metrics data", "error", err)
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					logging.Error(pluginCtx, "stopping metrics export after repeated marshal failures", "errors", consecutiveErrors)
					return
				}
				select {
				case <-pluginCtx.Done():
					return
				case <-workerCtx.Done():
					return
				case <-ticker.C:
				}
				continue
			}

			if pluginCtx.Err() != nil || workerCtx.Err() != nil {
				return
			}

			if err := databaseContainer.RetrySet(pluginCtx, metricsKey, metricsDataJson, time.Hour*24*7); err != nil {
				logging.Error(pluginCtx, "error storing metrics data in Redis", "error", err)
				consecutiveErrors++
				if consecutiveErrors >= maxConsecutiveErrors {
					logging.Error(pluginCtx, "stopping metrics export after repeated Redis failures", "errors", consecutiveErrors)
					return
				}
				select {
				case <-pluginCtx.Done():
					return
				case <-workerCtx.Done():
					return
				case <-ticker.C:
				}
				continue
			}

			if consecutiveErrors > 0 {
				logging.Info(pluginCtx, "errors resolved with metrics export")
				consecutiveErrors = 0
			}
			atLeastOneRun.Store(true)
			select {
			case <-pluginCtx.Done():
				return
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	waitTicker := time.NewTicker(100 * time.Millisecond)
	defer waitTicker.Stop()
	for !atLeastOneRun.Load() {
		select {
		case <-workerCtx.Done():
			return
		case <-pluginCtx.Done():
			return
		case <-waitTicker.C:
		}
	}
}
