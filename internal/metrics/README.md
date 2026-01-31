# Metrics Package

This package handles metrics collection and exposure for Anklet. It provides real-time visibility into plugin states, host resources, and job statistics.

## Architecture Overview

The metrics system consists of:

1. **In-memory metrics storage** (`MetricsDataLock`) - Thread-safe storage for all metrics
2. **HTTP server** - Exposes metrics via `/metrics/v1` endpoint
3. **Redis export** (optional) - Exports metrics to Redis for aggregation across hosts

## Data Structures

### MetricsDataLock

The central metrics storage with an embedded `sync.RWMutex` for thread-safe access:

```go
type MetricsDataLock struct {
    sync.RWMutex
    MetricsData
}
```

**Important:** All reads and writes to metrics data must use proper locking:
- Use `m.Lock()` / `m.Unlock()` for writes
- Use `m.RLock()` / `m.RUnlock()` for reads

### Plugin Types

Two plugin metric types exist:

| Type | Usage | Fields |
|------|-------|--------|
| `PluginBase` | Receiver plugins | Name, PluginName, RepoName, OwnerName, Status, StatusSince |
| `Plugin` | Handler plugins | All PluginBase fields + job URLs, timestamps, and counters |

Handler plugins (`Plugin`) track additional metrics like last successful/failed/canceled runs and total counts.

## Plugin Status Values

| Status | Description |
|--------|-------------|
| `idle` | Plugin is waiting for work (normal idle state) |
| `paused` | Plugin is waiting for another plugin to finish |
| `pulling` | Plugin is pulling a VM template from the registry |
| `running` | Plugin is actively running (post-pull) |
| `in_progress` | Plugin is processing a job |
| `stopped` | Plugin has been stopped (shutdown) |

### Status Flow

```
idle → paused → idle → in_progress → idle
         ↓
      pulling → running → in_progress → idle
```

## Thread Safety

The metrics system uses `sync.RWMutex` for thread safety:

### Writing Metrics

All write operations acquire the write lock:

```go
func (m *MetricsDataLock) SetStatus(pluginCtx context.Context, status string) error {
    m.Lock()
    defer m.Unlock()
    // ... modify metrics
}
```

### Reading Metrics

The HTTP handlers must acquire a read lock before accessing metrics:

```go
func (s *Server) handlePrometheusMetrics(...) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        metricsData := ctx.Value(...)
        metricsData.RLock()
        defer metricsData.RUnlock()
        // ... read metrics
    }
}
```

**Warning:** Failing to acquire locks when reading can cause data races where stale values are returned.

## Key Functions

### SetStatus

Updates a plugin's status in the metrics:

```go
func (m *MetricsDataLock) SetStatus(pluginCtx context.Context, status string) error
```

- Retrieves plugin name from context
- Only updates if the status is actually changing
- Updates `StatusSince` timestamp when status changes

### AddPlugin

Registers a new plugin in the metrics system:

```go
func (m *MetricsDataLock) AddPlugin(plugin any) error
```

- Called once per plugin at startup
- Checks for duplicates by name
- Accepts either `Plugin` or `PluginBase` types

### Increment Functions

Various functions to increment counters:

- `IncrementTotalRunningVMs()` - Increment running VM count
- `IncrementTotalSuccessfulRunsSinceStart()` - Increment success counter
- `IncrementTotalFailedRunsSinceStart()` - Increment failure counter
- `IncrementTotalCanceledRunsSinceStart()` - Increment cancellation counter

Each also updates the per-plugin counters.

## HTTP Endpoints

### /metrics/v1

The main metrics endpoint. Requires a `format` query parameter:

| Format | Content-Type | Description |
|--------|--------------|-------------|
| `prometheus` | `text/plain` | Prometheus-compatible format |

Example: `GET /metrics/v1?format=prometheus`

### Prometheus Output Format

```
plugin_status{name=MyRunner,plugin=github,owner=MyOrg} idle
plugin_status_since{name=MyRunner,plugin=github,owner=MyOrg} 2024-01-15T10:30:00Z
plugin_total_ran_vms{name=MyRunner,plugin=github,owner=MyOrg} 42
plugin_total_successful_runs_since_start{name=MyRunner,plugin=github,owner=MyOrg} 40
plugin_total_failed_runs_since_start{name=MyRunner,plugin=github,owner=MyOrg} 2
plugin_total_canceled_runs_since_start{name=MyRunner,plugin=github,owner=MyOrg} 0
host_cpu_count 8
host_cpu_used_count 2
host_cpu_usage_percentage 25.5
host_memory_total_bytes 17179869184
host_memory_used_bytes 8589934592
host_memory_available_bytes 8589934592
host_memory_usage_percentage 50.0
host_disk_total_bytes 500000000000
host_disk_used_bytes 250000000000
host_disk_available_bytes 250000000000
host_disk_usage_percentage 50.0
```

## Redis Export

For multi-host deployments, metrics can be exported to Redis for aggregation:

```go
func ExportMetricsToDB(workerCtx context.Context, pluginCtx context.Context, keyEnding string)
```

- Exports every 10 seconds
- Key format: `anklet/metrics/{owner}/{name}`
- TTL: 7 days
- Includes `last_update` timestamp for freshness checks

## Host Metrics

System metrics are collected using `gopsutil`:

| Metric | Source |
|--------|--------|
| CPU count/usage | `cpu.Counts()`, `cpu.Percent()` |
| Memory stats | `mem.VirtualMemory()` |
| Disk stats | `disk.Usage("/")` |

These are updated on each metrics request via `UpdateSystemMetrics()`.

## Cleanup

When a plugin shuts down, call `Cleanup()` to remove its metrics from Redis:

```go
func Cleanup(ctx context.Context, owner string, name string)
```

This deletes the `anklet/metrics/{owner}/{name}` key from Redis.
