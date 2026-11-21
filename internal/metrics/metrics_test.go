package metrics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

type fakeRedisMetrics struct {
	redis.Cmdable
	data map[string]string
}

func (f *fakeRedisMetrics) Scan(_ context.Context, cursor uint64, _ string, _ int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(context.Background(), nil)
	if cursor != 0 {
		cmd.SetVal(nil, 0)
		return cmd
	}
	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		keys = append(keys, k)
	}
	cmd.SetVal(keys, 0)
	return cmd
}

func (f *fakeRedisMetrics) Get(_ context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	val, ok := f.data[key]
	if !ok {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(val)
	return cmd
}

func TestSetStatusUpdatesStatusAndStatusSince(t *testing.T) {
	t.Parallel()

	m := &MetricsDataLock{}
	initialTime := time.Now().Add(-time.Hour)
	if err := m.AddPlugin(Plugin{
		PluginBase: PluginBase{
			Name:        "RUNNER1",
			PluginName:  "github",
			OwnerName:   "veertuinc",
			Status:      "idle",
			StatusSince: initialTime,
		},
	}); err != nil {
		t.Fatalf("AddPlugin() error = %v", err)
	}

	ctx := context.WithValue(context.Background(), config.ContextKey("plugin"), config.Plugin{Name: "RUNNER1"})
	if err := m.SetStatus(ctx, "limit_paused"); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	snap := m.Snapshot()
	if len(snap.Plugins) != 1 {
		t.Fatalf("len(Plugins) = %d, want 1", len(snap.Plugins))
	}
	if snap.Plugins[0].Status != "limit_paused" {
		t.Errorf("Status = %q, want limit_paused", snap.Plugins[0].Status)
	}
	if !snap.Plugins[0].StatusSince.After(initialTime) {
		t.Errorf("StatusSince was not updated: got %v, initial %v", snap.Plugins[0].StatusSince, initialTime)
	}
}

func TestMetricsConcurrentUpdatesAndPrometheusScrape(t *testing.T) {
	t.Parallel()

	m := &MetricsDataLock{}
	if err := m.AddPlugin(Plugin{
		PluginBase: PluginBase{
			Name:        "RUNNER1",
			PluginName:  "github",
			OwnerName:   "veertuinc",
			Status:      "idle",
			StatusSince: time.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	baseCtx := testContextWithLogger()
	workerCtx := context.WithValue(baseCtx, config.ContextKey("metrics"), m)
	pluginCtx := context.WithValue(baseCtx, config.ContextKey("plugin"), config.Plugin{Name: "RUNNER1"})
	srv := NewServer("0")
	handler := srv.Handler(workerCtx, false)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 15 {
				UpdateSystemMetrics(pluginCtx, m)
				_ = m.SetStatus(pluginCtx, "in_progress")
				m.IncrementTotalRunningVMs(workerCtx, pluginCtx)
				m.DecrementTotalRunningVMs()
				_ = m.SetStatus(pluginCtx, "idle")
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/metrics/v1?format=prometheus", nil)
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}
			}
		}()
	}
	wg.Wait()
}

func TestPrometheusSanitizesLabelsAndIsolatesServeMux(t *testing.T) {
	t.Parallel()

	m := &MetricsDataLock{}
	if err := m.AddPlugin(Plugin{
		PluginBase: PluginBase{
			Name:        "RUNNER1\ninjected",
			PluginName:  "github",
			OwnerName:   "org,bad}",
			Status:      "idle",
			StatusSince: time.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	workerCtx := context.WithValue(testContextWithLogger(), config.ContextKey("metrics"), m)
	handler := NewServer("0").Handler(workerCtx, false)

	// Webhook path must NOT be mounted on metrics mux.
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, httptest.NewRequest(http.MethodPost, "/jobs/v1/receiver", nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("/jobs/v1/receiver on metrics mux status = %d, want 404", rec404.Code)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics/v1?format=prometheus", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "plugin_status{name=RUNNER1injected,plugin=github,owner=orgbad} idle\n") {
		t.Fatalf("unexpected prometheus output:\n%s", body)
	}
}

func TestAggregatorHandlesV1V2PrometheusAndSkipsCorruptEntries(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	validMetrics := MetricsData{
		LastUpdate:   now,
		HostCPUCount: 12,
		Plugins: []Plugin{
			{
				PluginBase: PluginBase{
					Name:        "RUNNER1",
					PluginName:  "github",
					OwnerName:   "veertuinc",
					Status:      "idle",
					StatusSince: now,
				},
				TotalRanVMs: 3,
			},
			{
				PluginBase: PluginBase{
					Name:        "RECEIVER1",
					PluginName:  "github_receiver",
					OwnerName:   "veertuinc",
					IsReceiver:  true,
					Status:      "idle",
					StatusSince: now,
				},
			},
		},
	}
	validJSON, err := json.Marshal(validMetrics)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeRedisMetrics{
		data: map[string]string{
			"anklet/metrics/veertuinc/RUNNER1": string(validJSON),
			"anklet/metrics/veertuinc/CORRUPT": "{not-valid-json",
		},
	}
	db := &database.Database{Client: fake, MaxRetries: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.WithValue(context.Background(), config.ContextKey("database"), db)
	handler := NewServer("0").AggregatorHandler(ctx, logger)

	// V1 JSON
	recV1 := httptest.NewRecorder()
	handler.ServeHTTP(recV1, httptest.NewRequest(http.MethodGet, "/metrics/v1?format=json", nil))
	if recV1.Code != http.StatusOK {
		t.Fatalf("v1 status = %d, want 200", recV1.Code)
	}
	var v1 map[string]jsonMetricsResponse
	if err := json.Unmarshal(recV1.Body.Bytes(), &v1); err != nil {
		t.Fatalf("v1 unmarshal error = %v", err)
	}
	if v1["RUNNER1"].PluginTotalRanVMs != 3 || v1["RECEIVER1"].PluginName != "github_receiver" {
		t.Fatalf("unexpected v1 payload: %+v", v1)
	}

	// V2 JSON
	recV2 := httptest.NewRecorder()
	handler.ServeHTTP(recV2, httptest.NewRequest(http.MethodGet, "/metrics/v2?format=json", nil))
	if recV2.Code != http.StatusOK {
		t.Fatalf("v2 status = %d, want 200", recV2.Code)
	}
	var v2 []jsonMetricsResponse
	if err := json.Unmarshal(recV2.Body.Bytes(), &v2); err != nil {
		t.Fatalf("v2 unmarshal error = %v", err)
	}
	if len(v2) != 2 {
		t.Fatalf("len(v2) = %d, want 2", len(v2))
	}

	// Prometheus
	recProm := httptest.NewRecorder()
	handler.ServeHTTP(recProm, httptest.NewRequest(http.MethodGet, "/metrics/v1?format=prometheus", nil))
	if recProm.Code != http.StatusOK {
		t.Fatalf("prometheus status = %d, want 200", recProm.Code)
	}
	if !strings.Contains(recProm.Body.String(), "plugin_total_ran_vms{name=RUNNER1,plugin=github,owner=veertuinc} 3") {
		t.Fatalf("missing handler counter in aggregator prometheus output:\n%s", recProm.Body.String())
	}

	// Healthcheck
	recHealth := httptest.NewRecorder()
	handler.ServeHTTP(recHealth, httptest.NewRequest(http.MethodGet, "/healthcheck", nil))
	if recHealth.Code != http.StatusOK || recHealth.Body.String() != "ok" {
		t.Fatalf("healthcheck = %d %q, want 200 ok", recHealth.Code, recHealth.Body.String())
	}
}

type fakeRedisMetricsRW struct {
	fakeRedisMetrics
	mu sync.Mutex
}

func (f *fakeRedisMetricsRW) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStatusCmd(context.Background())
	switch v := value.(type) {
	case []byte:
		f.data[key] = string(v)
	case string:
		f.data[key] = v
	}
	cmd.SetVal("OK")
	return cmd
}

func (f *fakeRedisMetricsRW) Del(_ context.Context, keys ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	var deleted int64
	for _, k := range keys {
		if _, ok := f.data[k]; ok {
			delete(f.data, k)
			deleted++
		}
	}
	cmd.SetVal(deleted)
	return cmd
}

func TestUpdatePluginMonotonicityCountersCleanupAndExportHappyPath(t *testing.T) {
	t.Parallel()

	m := &MetricsDataLock{}
	if err := m.AddPlugin(Plugin{
		PluginBase: PluginBase{Name: "H1", PluginName: "github", OwnerName: "veertuinc", Status: "idle"},
	}); err != nil {
		t.Fatal(err)
	}

	pluginCtx := context.WithValue(testContextWithLogger(), config.ContextKey("plugin"), config.Plugin{Name: "H1"})
	workerCtx, workerCancel := context.WithCancel(context.WithValue(testContextWithLogger(), config.ContextKey("metrics"), m))
	defer workerCancel()

	m.IncrementTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx)
	m.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
	m.IncrementTotalCanceledRunsSinceStart(workerCtx, pluginCtx)

	now := time.Now()
	if err := m.UpdatePlugin(workerCtx, pluginCtx, Plugin{
		PluginBase:              PluginBase{Name: "H1", Status: "in_progress"},
		LastSuccessfulRun:       now,
		LastSuccessfulRunJobUrl: "https://github.com/org/repo/job/1",
		LastFailedRun:           now,
		LastFailedRunJobUrl:     "https://github.com/org/repo/job/2",
		LastCanceledRun:         now,
		LastCanceledRunJobUrl:   "https://github.com/org/repo/job/3",
		TotalRanVMs:             5,
	}); err != nil {
		t.Fatal(err)
	}
	// Smaller counter must not decrease an existing counter.
	if err := m.UpdatePlugin(workerCtx, pluginCtx, Plugin{
		PluginBase:  PluginBase{Name: "H1"},
		TotalRanVMs: 2,
	}); err != nil {
		t.Fatal(err)
	}

	snap := m.Snapshot()
	if snap.TotalSuccessfulRunsSinceStart != 1 || snap.TotalFailedRunsSinceStart != 1 || snap.TotalCanceledRunsSinceStart != 1 {
		t.Fatalf("unexpected global run totals: %+v", snap)
	}
	if snap.Plugins[0].TotalRanVMs != 5 || snap.Plugins[0].LastSuccessfulRunJobUrl != "https://github.com/org/repo/job/1" {
		t.Fatalf("unexpected plugin metrics: %+v", snap.Plugins[0])
	}

	// Happy-path ExportMetricsToDB + Cleanup.
	fake := &fakeRedisMetricsRW{fakeRedisMetrics: fakeRedisMetrics{data: map[string]string{}}}
	db := &database.Database{Client: fake, MaxRetries: 0}
	exportCtx, exportCancel := context.WithCancel(context.WithValue(pluginCtx, config.ContextKey("database"), db))

	ExportMetricsToDB(workerCtx, exportCtx, "veertuinc/H1")
	exportCancel()

	fake.mu.Lock()
	_, exists := fake.data["anklet/metrics/veertuinc/H1"]
	fake.mu.Unlock()
	if !exists {
		t.Fatal("ExportMetricsToDB did not write anklet/metrics/veertuinc/H1")
	}

	cleanupCtx := context.WithValue(testContextWithLogger(), config.ContextKey("database"), db)
	Cleanup(cleanupCtx, "veertuinc", "H1")
	fake.mu.Lock()
	_, stillExists := fake.data["anklet/metrics/veertuinc/H1"]
	fake.mu.Unlock()
	if stillExists {
		t.Fatal("Cleanup did not delete anklet/metrics/veertuinc/H1")
	}
}

func TestStartWithListenerAndAggregatorGracefulShutdown(t *testing.T) {
	t.Parallel()

	m := &MetricsDataLock{}
	_ = m.AddPlugin(Plugin{PluginBase: PluginBase{Name: "H1", PluginName: "github", Status: "idle"}})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(testContextWithLogger(), config.ContextKey("metrics"), m))
	done := make(chan struct{})
	srv := NewServerWithAddress("127.0.0.1", "0")
	go func() {
		defer close(done)
		srv.StartWithListener(ctx, ln, false)
	}()

	resp, err := http.Get("http://" + ln.Addr().String() + "/metrics/v1?format=prometheus")
	if err != nil {
		cancel()
		t.Fatalf("GET metrics failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartWithListener did not shut down cleanly")
	}

	// Aggregator server listener shutdown.
	aggLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	aggCtx, aggCancel := context.WithCancel(context.Background())
	aggDone := make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() {
		defer close(aggDone)
		NewServerWithAddress("127.0.0.1", "0").StartAggregatorServerWithListener(aggCtx, logger, aggLn)
	}()

	hResp, err := http.Get("http://" + aggLn.Addr().String() + "/healthcheck")
	if err != nil {
		aggCancel()
		t.Fatalf("GET /healthcheck failed: %v", err)
	}
	_ = hResp.Body.Close()
	if hResp.StatusCode != http.StatusOK {
		t.Errorf("aggregator /healthcheck status = %d, want 200", hResp.StatusCode)
	}
	aggCancel()
	select {
	case <-aggDone:
	case <-time.After(3 * time.Second):
		t.Fatal("StartAggregatorServerWithListener did not shut down cleanly")
	}
}
