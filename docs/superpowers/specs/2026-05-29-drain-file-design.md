# Drain File Design

Implements [veertuinc/anklet#90](https://github.com/veertuinc/anklet/issues/90).

## Problem

Anklet handler hosts are limited to 2 running VMs at a time (Apple SLA). When an
operator needs to use a host for other VM work outside Anklet (e.g. building a
new template/image), Anklet handlers will keep claiming jobs and spinning up
VMs. This causes `more than 2 VMs are running; unable to run more than 2 at a
time due to Apple SLA` failures.

Operators need a way to tell handlers on a host to stop picking up *new* jobs,
let in-flight jobs drain naturally, free up VM capacity, do their manual work,
and then resume — without restarting or reconfiguring Anklet.

## Solution Overview

A **drain file**: a fixed, well-known path that, when present, tells handler
plugins on that host to stop picking up new jobs while remaining running.
In-progress jobs continue to completion and clean up normally. Removing the file
resumes normal operation on the next loop iteration.

- **Drain file path (fixed):** `~/.config/anklet/.drain` (resolved via
  `os.UserHomeDir()`).
- **Scope:** Handlers only. Receiver plugins are unaffected and keep queuing
  jobs in Redis; those jobs are picked up once the drain file is removed.
- **Trigger:** File existence only. Contents are ignored. Operators run
  `touch ~/.config/anklet/.drain` to drain and `rm ~/.config/anklet/.drain` to
  resume.

## Architecture

### Shared helper (DRY)

A single source of truth for "is this host draining?" so `main.go` and the
handler plugin do not duplicate the `os.Stat` logic.

- `DrainFilePath() (string, error)` — returns `~/.config/anklet/.drain`,
  resolved from `os.UserHomeDir()`.
- `IsDraining() bool` — returns `true` if the drain file exists. Any error
  resolving the path or stat-ing the file (other than "exists") is treated as
  **not draining** (fail-open), to avoid wedging a host on a transient error.
  Returning a `bool` keeps call sites simple (mirrors the existing
  `HostHasVmCapacity(pluginCtx) bool` pattern). `IsDraining()` does not log (it
  has no context dependency); call sites do the user-facing logging.

Placement: a new small dedicated package `internal/drain`. It depends only on
the standard library (`os`, `path/filepath`), so both `main.go` and the
`plugins/handlers/github` package can import it with no import cycle. A
dedicated package keeps the responsibility clearly named rather than overloading
`internal/anka` (which is Anka-CLI-specific).

### Handler integration

In `plugins/handlers/github/github.go`, immediately **before** the existing VM
capacity check (`HostHasVmCapacity`, ~line 1477):

```go
if drain.IsDraining() {
    drainPath, _ := drain.DrainFilePath()
    logging.Warn(pluginCtx, "drain file present; not picking up new jobs", "path", drainPath)
    if err := metricsData.SetStatus(pluginCtx, "draining"); err != nil {
        logging.Error(pluginCtx, "error setting plugin status", "error", err)
    }
    pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
    return pluginCtx, nil
}
```

This placement is deliberate: it runs **after** the orphaned/in-progress job
cleanup and `watchJobStatus` logic earlier in `Run()`, so a job already being
handled still completes and cleans up. Only the pickup of a *new* queued job is
skipped. Behavior mirrors the existing "host at capacity" early return
(`finish` action + `return pluginCtx, nil`).

### Status persistence in main.go

`main.go`'s plugin loop sets the plugin status to `"idle"` after every
`run.Plugin` returns (~line 766) and again before each run (~line 730). A
`"draining"` status set inside `Run()` would be immediately overwritten back to
`"idle"`.

Fix: at those `SetStatus(..., "idle")` sites, set `"draining"` when
`IsDraining()` is true, otherwise `"idle"`. While the drain file exists,
draining handlers report `"draining"` between runs instead of flickering back to
`idle`.

This applies to handler plugins. Receiver status handling is left unchanged.

## Data Flow

1. Operator runs `touch ~/.config/anklet/.drain`.
2. Handler `Run()` is invoked by the loop. Orphaned/in-progress cleanup runs as
   usual; any job currently in progress is watched to completion.
3. Before claiming a *new* job, `IsDraining()` returns `true`. The handler logs,
   sets status `"draining"`, emits a `finish` action, and returns.
4. `main.go` keeps the status as `"draining"` (not `idle`) between runs.
5. VM capacity frees up as in-flight jobs finish; operator does manual VM work.
6. Operator runs `rm ~/.config/anklet/.drain`.
7. Next loop iteration: `IsDraining()` returns `false`; handler resumes normal
   job pickup and status returns to `idle`/`running`.

## Error Handling

- `os.UserHomeDir()` failure in `DrainFilePath()`: propagate the error to
  callers that need the path (e.g. for logging). `IsDraining()` treats an
  unresolvable path as **not draining** (fail-open) so a broken home dir cannot
  silently halt all job processing.
- `os.Stat` errors other than the file existing: treated as not draining.
  Fail-open is the safe default — a transient stat error should not stop a
  production fleet from working.
- `metricsData.SetStatus` failure: logged, non-fatal (consistent with all other
  `SetStatus` call sites).

## Metrics

New status string value `"draining"`. `SetStatus` accepts free-form strings, so
no enum/validation change is required. The value surfaces through the existing
metrics endpoint and aggregator unchanged.

## Testing

- **Unit:** `IsDraining()` returns `false` when the file is absent and `true`
  when present, driven by a temp HOME via `t.Setenv("HOME", tmpDir)`.
  `DrainFilePath()` resolves to `<HOME>/.config/anklet/.drain`.
- **CLI integration:** A **new** test directory (modeled on
  `core-tests/4-test-vm-capacity-no-exit`, which asserts Anklet stays up and
  doesn't pick up jobs). The test: queue a job, `touch` the drain file, assert
  the job is not picked up and the plugin reports `draining` while Anklet stays
  running, then `rm` the drain file and assert the job runs to completion.
  Existing test files (`manifest.yaml`, `test.bash`) are not modified — only new
  files are added.

## Documentation

Add a "Draining a host" section to `README.md` describing:
- The fixed path `~/.config/anklet/.drain`.
- The `touch` (drain) / `rm` (resume) workflow.
- That only handlers are affected, in-progress jobs finish, and receivers keep
  queuing.

## Out of Scope (YAGNI)

- Configurable / per-plugin drain paths (host-wide fixed path chosen).
- Draining receiver plugins.
- A graceful-shutdown "drain then exit" signal (distinct from this feature;
  there is an unrelated commented-out signal stub in `main.go`).
