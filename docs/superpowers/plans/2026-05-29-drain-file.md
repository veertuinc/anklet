# Drain File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a host-wide drain file (`~/.config/anklet/.drain`) that stops GitHub handler plugins from picking up new jobs (while staying running and finishing in-progress jobs) so operators can free VM capacity for manual work, then resume by removing the file.

**Architecture:** A new dependency-free `internal/drain` package exposes `DrainFilePath()` and `IsDraining()`. The handler's `Run()` checks `IsDraining()` right before the existing VM-capacity check and returns early (emitting a `finish` action) when draining. `main.go`'s post-run status set reports `"draining"` instead of `"idle"` for handler plugins while the file exists, so the status persists between loop iterations.

**Tech Stack:** Go 1.24, standard library only for the new package. Existing `internal/logging`, `internal/metrics`, `internal/github` for integration.

**Spec:** `docs/superpowers/specs/2026-05-29-drain-file-design.md`

---

### Task 1: `internal/drain` package + unit tests

**Files:**
- Create: `internal/drain/drain.go`
- Test: `internal/drain/drain_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/drain/drain_test.go`:

```go
package drain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDrainFilePath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	got, err := DrainFilePath()
	if err != nil {
		t.Fatalf("DrainFilePath() returned error: %v", err)
	}
	want := filepath.Join(tmpHome, ".config", "anklet", ".drain")
	if got != want {
		t.Errorf("DrainFilePath() = %q, want %q", got, want)
	}
}

func TestIsDrainingFalseWhenAbsent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if IsDraining() {
		t.Errorf("IsDraining() = true with no drain file, want false")
	}
}

func TestIsDrainingTrueWhenPresent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	drainPath := filepath.Join(tmpHome, ".config", "anklet", ".drain")
	if err := os.MkdirAll(filepath.Dir(drainPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(drainPath, []byte{}, 0o644); err != nil {
		t.Fatalf("failed to create drain file: %v", err)
	}

	if !IsDraining() {
		t.Errorf("IsDraining() = false with drain file present, want true")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/drain/...`
Expected: FAIL — build error, `undefined: DrainFilePath` / `undefined: IsDraining`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/drain/drain.go`:

```go
// Package drain provides a host-wide "drain" signal: when the drain file
// exists, handler plugins stop picking up new jobs while continuing to run and
// finish in-progress work. Operators touch the file to drain a host and remove
// it to resume.
package drain

import (
	"os"
	"path/filepath"
)

// DrainFilePath returns the fixed, well-known drain file location:
// ~/.config/anklet/.drain
func DrainFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".config", "anklet", ".drain"), nil
}

// IsDraining reports whether the drain file exists. It fails open: any error
// resolving the path or stat-ing the file (other than the file existing) is
// treated as "not draining" so a transient error cannot silently halt all job
// processing on a host.
func IsDraining() bool {
	path, err := DrainFilePath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/drain/...`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/drain/drain.go internal/drain/drain_test.go
git commit -m "Add internal/drain package for host-wide drain file (#90)"
```

---

### Task 2: Handler honors the drain file

**Files:**
- Modify: `plugins/handlers/github/github.go` (import block ~lines 16-23; insert before the VM-capacity check at ~line 1474)

- [ ] **Step 1: Add the import**

In the import block (after the other `internal/...` imports, keep alphabetical-ish grouping), add:

```go
	"github.com/veertuinc/anklet/internal/drain"
```

So the block reads (relevant lines):

```go
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/drain"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
```

- [ ] **Step 2: Insert the drain check before the VM-capacity check**

Find this existing block (~line 1474):

```go
	// Reject new jobs (but keep anklet running) when the host is at VM capacity.
	// Apple's SLA limits us to 2 running VMs, and Apple can leave behind orphaned
	// Virtualization processes; in that case we want to wait it out rather than exit.
	hostHasVmCapacity := internalAnka.HostHasVmCapacity(pluginCtx)
```

Insert immediately ABOVE that comment:

```go
	// Reject new jobs (but keep anklet running) when the host is draining.
	// Operators create the drain file to free VM capacity for manual work
	// (e.g. building templates/images) without stopping anklet entirely.
	// In-progress jobs handled earlier in Run() still finish and clean up.
	if drain.IsDraining() {
		drainFilePath, _ := drain.DrainFilePath()
		logging.Warn(pluginCtx, "drain file present; not picking up new jobs", "path", drainFilePath)
		if err := metricsData.SetStatus(pluginCtx, "draining"); err != nil {
			logging.Error(pluginCtx, "error setting plugin status", "error", err)
		}
		pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}
		return pluginCtx, nil
	}

```

Note: `metricsData` (defined ~line 1271), `pluginGlobals` (defined ~line 1292), and `logging` are all in scope in `Run()`.

- [ ] **Step 3: Build and lint**

Run: `make go.lint`
Expected: no errors (the linter also gofmt-orders imports; let it).

- [ ] **Step 4: Run unit tests**

Run: `make go.test`
Expected: PASS (no regressions; existing handler tests still pass).

- [ ] **Step 5: Commit**

```bash
git add plugins/handlers/github/github.go
git commit -m "Handler skips new jobs when drain file present (#90)"
```

---

### Task 3: Persist `"draining"` status in main.go

**Files:**
- Modify: `main.go` (import block ~lines 23-29; add helper func; change two `SetStatus(..., "idle")` sites at ~line 730 and ~line 766)

- [ ] **Step 1: Add the import**

In `main.go`'s import block, add (alongside the other `internal/...` imports):

```go
	"github.com/veertuinc/anklet/internal/drain"
```

- [ ] **Step 2: Add a DRY helper for the steady-state status**

Add this function at package scope (near the other top-level helpers in `main.go`, e.g. just above `func main()`):

```go
// steadyStatus returns the status a plugin should report between runs:
// "draining" for handler plugins while the drain file is present, otherwise
// "idle". Receiver plugins are never marked draining (they only queue jobs).
func steadyStatus(plugin config.Plugin) string {
	if !strings.Contains(plugin.Plugin, "_receiver") && drain.IsDraining() {
		return "draining"
	}
	return "idle"
}
```

(`strings` and `config` are already imported in `main.go`.)

- [ ] **Step 3: Update the post-preparing idle set (~line 730)**

Find:

```go
						// Set status back to idle after exiting the preparing wait loop
						err = metricsData.SetStatus(pluginCtx, "idle")
						if err != nil {
							logging.Error(pluginCtx, "error setting plugin status", "error", err)
						}
```

Replace the `SetStatus` line so it reads:

```go
						// Set status back to idle (or draining) after exiting the preparing wait loop
						err = metricsData.SetStatus(pluginCtx, steadyStatus(plugin))
						if err != nil {
							logging.Error(pluginCtx, "error setting plugin status", "error", err)
						}
```

- [ ] **Step 4: Update the post-run idle set (~line 766)**

Find:

```go
						err = metricsData.SetStatus(updatedPluginCtx, "idle")
						if err != nil {
							logging.Error(updatedPluginCtx, "error setting plugin status", "error", err)
						}
```

Replace the `SetStatus` line so it reads:

```go
						err = metricsData.SetStatus(updatedPluginCtx, steadyStatus(plugin))
						if err != nil {
							logging.Error(updatedPluginCtx, "error setting plugin status", "error", err)
						}
```

- [ ] **Step 5: Lint and test**

Run: `make go.lint && make go.test`
Expected: no errors; tests PASS.

- [ ] **Step 6: Commit**

```bash
git add main.go
git commit -m "Report draining status for handlers while drain file present (#90)"
```

---

### Task 4: Document the drain file in README

**Files:**
- Modify: `README.md` (insert a new `## Draining a host` section just before `## Upgrading` at ~line 676)

- [ ] **Step 1: Insert the documentation section**

Immediately ABOVE the `## Upgrading` heading (line ~676), insert:

```markdown
## Draining a host

Sometimes you need to use an Anklet host for VM work outside of Anklet (for
example, building a new template/image). Because Apple's SLA limits a host to
**2 running VMs at a time**, Anklet handlers competing for that capacity can
cause `more than 2 VMs are running` failures.

To temporarily stop handlers on a host from picking up **new** jobs without
stopping Anklet, create the **drain file**:

```bash
touch ~/.config/anklet/.drain
```

While the drain file exists:

- Handler plugins on that host stop claiming new jobs and report a `draining`
  status in metrics. The log shows `drain file present; not picking up new jobs`.
- Jobs already in progress continue to run and clean up normally, so VM
  capacity drains naturally.
- Receiver plugins are unaffected and keep queuing jobs in Redis; those jobs are
  picked up once you resume.

Once capacity is free, do your manual VM work. To resume normal operation,
remove the file:

```bash
rm ~/.config/anklet/.drain
```

Handlers pick up jobs again on their next loop iteration. No restart required.

---
```

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "Document drain file workflow in README (#90)"
```

---

### Task 5 (cross-repo, confirm before doing): CLI integration test

> This test lives in the separate **anklet-tests** repo, not `anklet`. Per that
> repo's rules, changes outside `anklet/tests/` should be confirmed first, and
> existing test files must not be edited. This task ONLY creates a new test
> directory. Confirm with the maintainer before running CI for it.

**Files (in `/Users/nathanpierce/anklet-tests`):**
- Create: `core-tests/5-test-drain-file/manifest.yaml`
- Create: `core-tests/5-test-drain-file/test.bash`
- Create: `core-tests/5-test-drain-file/helpers.bash` (copy the pattern used by `core-tests/4-test-vm-capacity-no-exit/helpers.bash`)

- [ ] **Step 1: Create `manifest.yaml`** (model: `core-tests/4-test-vm-capacity-no-exit/manifest.yaml`)

```yaml
description: "Drain file: handler stays running and keeps the job queued while the drain file is present"
tests:
    - name: "Drain file test"
      hosts:

        - name: "receiver"
          id: "ubuntu-22.04-linux"
          config: ubuntu-22.04-linux.yaml

        - name: "handler-8-16"
          id: "13-L-ARM-macos"
          config: 13-L-ARM-macos.yaml
          tester: true
```

- [ ] **Step 2: Create `test.bash`** (model: `core-tests/4-test-vm-capacity-no-exit/test.bash`)

The flow (reusing the same shared helpers — `init_test_report`, `begin_test`,
`start_anklet_on_host_background`, `start_anklet_backgrounded_but_attached`,
`trigger_workflow_runs`, `assert_redis_key_exists`, `assert_logs_contain`,
`wait_for_workflow_runs_to_complete`, `record_pass`, `end_test`,
`finalize_test_report`, `interrupt_anklet_processes`):

```bash
#!/usr/bin/env bash
set -eo pipefail
TEST_DIR_NAME="$(basename "$(pwd)")"
echo "==========================================="
echo "START $TEST_DIR_NAME/test.bash"
echo "==========================================="
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/helpers.bash"

init_test_report "${TEST_DIR_NAME}"

DRAIN_FILE="${HOME}/.config/anklet/.drain"
WORKFLOW_FILE="t1-with-tag-1.yml"
WORKFLOW_PATTERN="t1-with-tag-1"
MAIN_QUEUE_KEY="anklet/jobs/github/queued/veertuinc"

assert_log_count_at_least() {
    local pattern="$1" min_count="$2" log_file="${3:-/tmp/anklet.log}" count
    count=$(grep -c "$pattern" "$log_file" 2>/dev/null || true)
    count=${count:-0}
    if [[ "${count}" -ge "${min_count}" ]]; then
        echo "] ✓ '${pattern}' found ${count} time(s) (expected >= ${min_count})"
        return 0
    fi
    echo "] ✗ '${pattern}' found ${count} time(s) (expected >= ${min_count})"
    record_fail "'${pattern}' found ${count} time(s), expected >= ${min_count}"
    return 1
}

assert_anklet_running() {
    if pgrep -f '^/tmp/anklet$' > /dev/null 2>&1; then
        echo "] ✓ anklet process is still running"
        return 0
    fi
    echo "] ✗ anklet process is not running (handler exited)"
    record_fail "anklet process exited while drain file was present"
    return 1
}

cleanup() {
    echo ""
    echo "==========================================="
    echo "START $TEST_DIR_NAME/test.bash cleanup..."
    rm -f "${DRAIN_FILE}" || true
    echo "] Cancelling running workflow runs..."
    cancel_running_workflow_runs "veertuinc" "anklet" "t1-" "t2-" || echo "WARNING: Some workflow cancellations may have failed"
    echo "] Killing the anklet process..."
    interrupt_anklet_processes
    echo "END $TEST_DIR_NAME/test.bash cleanup..."
    echo "==========================================="
}
trap 'cleanup;' EXIT

begin_test "Drain file test"

echo "] Step 0: Removing any leftover drain file..."
rm -f "${DRAIN_FILE}"

echo "] Step 1: Creating the drain file before starting anklet..."
mkdir -p "$(dirname "${DRAIN_FILE}")"
touch "${DRAIN_FILE}"

echo "] Step 2: Starting anklet services (receiver remote, handler local)..."
start_anklet_on_host_background "receiver"
start_anklet_backgrounded_but_attached "handler-8-16"

echo "] Step 3: Triggering ${WORKFLOW_FILE} so a job gets queued while draining..."
trigger_workflow_runs "veertuinc" "anklet" "${WORKFLOW_FILE}" 1

echo "] Step 4: Waiting for the receiver to queue the job in Redis..."
job_queued=false
for _ in $(seq 1 40); do
    if assert_redis_key_exists "${MAIN_QUEUE_KEY}" > /dev/null 2>&1; then
        job_queued=true
        break
    fi
    sleep 3
done
if [[ "${job_queued}" != "true" ]]; then
    record_fail "job was never queued to ${MAIN_QUEUE_KEY}; cannot verify drain behavior"
    exit 1
fi

echo "] Step 5: Letting the handler run a few cycles while draining..."
sleep 20

echo "] Step 6: Verifying the handler did NOT pick up the job while draining..."
assert_anklet_running
assert_logs_contain "drain file present; not picking up new jobs" /tmp/anklet.log
assert_log_count_at_least "drain file present; not picking up new jobs" 2 /tmp/anklet.log
assert_logs_not_contain "handling anka workflow run job" /tmp/anklet.log
if ! assert_redis_key_exists "${MAIN_QUEUE_KEY}"; then
    record_fail "job no longer queued in ${MAIN_QUEUE_KEY} while draining (handler consumed it)"
    exit 1
fi

echo "] Step 7: Removing the drain file so the job can run..."
rm -f "${DRAIN_FILE}"

echo "] Step 8: Waiting for the queued job to be picked up and complete successfully..."
wait_for_workflow_runs_to_complete "veertuinc" "anklet" "${WORKFLOW_PATTERN}" "success" 900

echo "] Step 9: Verifying the handler picked the job up after the drain file was removed..."
assert_logs_contain "handling anka workflow run job" /tmp/anklet.log

record_pass
end_test

echo "==========================================="
echo "END $TEST_DIR_NAME/test.bash"
echo "==========================================="

finalize_test_report "${TEST_DIR_NAME}"

if [[ $TEST_FAILED -gt 0 ]]; then
    exit 1
fi
```

- [ ] **Step 3: Create `helpers.bash`**

Copy `core-tests/4-test-vm-capacity-no-exit/helpers.bash` verbatim into the new
directory (it just sources the shared `/tmp/redis-functions.bash` test harness;
the existing tests use an identical per-directory `helpers.bash`).

- [ ] **Step 4: Commit (in the anklet-tests repo)**

```bash
git add core-tests/5-test-drain-file/
git commit -m "Add drain file CLI integration test"
```

---

### Task 6: Final verification

- [ ] **Step 1: Lint**

Run: `make go.lint`
Expected: no errors.

- [ ] **Step 2: Full unit test suite**

Run: `make go.test`
Expected: PASS, including `internal/drain`.

- [ ] **Step 3: Confirm the feature end to end (manual smoke, optional if no host)**

With a configured handler running locally:
- `touch ~/.config/anklet/.drain` → log shows `drain file present; not picking up new jobs`, metrics status `draining`, queued jobs stay queued.
- `rm ~/.config/anklet/.drain` → handler resumes, status returns to `idle`/`running`, queued job is picked up.
