# Development

## Prepare your environment for development

```bash
brew install go
go mod tidy
cd ${REPO_ROOT}
ln -s ~/.config/anklet/org-config.yml org-config.yml
ln -s ~/.config/anklet/repo-receiver-config.yml repo-receiver-config.yml
./run.bash org-receiver-config.yml # run the receiver
./run.bash org-config.yml # run the handler
```

- **NOTE:** You'll need to change the webhook URL so it points to the public IP of the server running the receiver (for me, that's my ISP's public IP + open port forwarding to my local machine).

## Building

Anklet requires **Go 1.24.1** (see `go.mod`). After `go mod tidy`, build from the repo root.

### Local binary (current OS/arch)

The fastest way to produce a binary for the machine you're on:

```bash
make go.build
```

This writes an executable to `dist/anklet_v<VERSION>_<os>_<arch>` (for example `dist/anklet_v1.5.0_darwin_arm64`). The version comes from the [`VERSION`](./VERSION) file and is embedded in the binary via `-ldflags "-X main.version=..."`.

Install that build to `~/bin/anklet`:

```bash
make install
```

(`install` expects `make go.build` to have been run first.)

Equivalent one-liner without Make:

```bash
go build -ldflags "-X main.version=$(cat VERSION)" -o dist/anklet .
```

### Linux binary (from macOS)

Handler plugins run on macOS; receiver plugins often run on Linux. To cross-compile a Linux binary from a Mac:

```bash
make build-linux
```

Override architecture with `ARCH=arm64 make build-linux` if needed.

### Snapshot and release builds

For multi-platform binaries (Linux amd64/arm64, Darwin amd64/arm64), use [GoReleaser](https://goreleaser.com/):

```bash
make build-snapshot   # snapshot binaries under dist/ (no git tag required)
make go.releaser      # tagged release build (tags VERSION, runs goreleaser release)
```

Release builds run `make go.lint` and `make go.test` first. Linux artifacts are statically linked via musl-cross (`brew install filosottile/musl-cross/musl-cross` if you do not already have it).

Other useful targets:

| Target | Description |
| --- | --- |
| `make go.lint` | `go vet` + golangci-lint (run after code changes) |
| `make go.test` | Unit tests |
| `make cross-compile-check` | Verify all GoReleaser target platforms compile |
| `make build-docker-snapshot` | Snapshot build + Docker image prep |

### Log Levels

| Level | Description |
| --- | --- |
| `INFO` | Default. Standard JSON output with info level messages. |
| `DEBUG` | JSON output with debug level messages. |
| `DEBUG-PRETTY` | Colored, pretty-printed output with debug level messages. Best for local development. |
| `DEV` | Alias for `DEBUG-PRETTY`. |
| `ERROR` | JSON output with only error level messages. |

The `DEBUG-PRETTY` (or `DEV`) log level has colored output with text + pretty printed JSON for easier debugging. Here is an example:

```
[20:45:21.814] INFO: job still in progress {
  "ankaTemplate": "d792c6f6-198c-470f-9526-9c998efe7ab4",
  "ankaTemplateTag": "vanilla+port-forward-22+brew-git",
  "ankletVersion": "dev",
  "jobURL": "https://github.com/veertuinc/anklet/actions/runs/8608565514/job/23591139958",
  "job_id": 23591139958,
  "owner": "veertuinc",
  "plugin": "github",
  "repo": "anklet",
  "serviceName": "RUNNER1",
  "source": {
    "file": "/Users/nathanpierce/anklet/plugins/handlers/github/github.go",
    "function": "github.com/veertuinc/anklet/plugins/handlers/github.Run",
    "line": 408
  },
  "uniqueRunKey": "8608565514:1",
  "vmName": "anklet-vm-83685657-9bda-4b32-84db-6c50ee712268",
  "workflowJobId": 23591139958,
  "workflowJobName": "testJob",
  "workflowName": "t1-with-tag-1",
  "workflowRunId": 8608565514,
  "workflowRunName": "t1-with-tag-1"
}
```

- `LOG_LEVEL=ERROR go run main.go` to see only errors
- Run each service only once with `LOG_LEVEL=DEBUG-PRETTY go run -ldflags "-X main.runOnce=true" main.go`

## Plugins

Plugins are, currently, stored in the `plugins/` directory. They will be moved into external binaries at some point in the future.

Plugins should follow a pattern of: `Pick up job from DB` -> `Run Job` -> `Cleanup Job` -> `Optionally Return to DB if it needs to be retried`. Advanced plugins can also handle pausing the plugin, waiting for the other job running to finish, if there are not enough resources to run the job on the host yet, and let others hosts that can run it take it from the paused queue.

Plugins are loaded in the order they are listed in the config.yml file. We use the `workerGlobals.Plugins[plugin.Plugin][plugin.Name].Paused.Store(true)` and `workerGlobals.Plugins[plugin.Plugin][plugin.Name].FinishedInitialRun.Store(true)` to handle this. Your plugin logic MUST set this to `false` when it finishes running/does the cleanup phase.

Each plugin will also wait others of its type to finish "preparing" before they start. Once it's safe to allow other plugins to start, the plugin logic must perform `workerGlobals.Plugins[plugin.Plugin][plugin.Name].Preparing.Store(false)`.

### Guidelines

**Important:** Avoid handling context cancellations in places of the code that will need to be done before the runner exits. This means any VM deletion or database cleanup must be done using functions that do not have context cancellation, allowing them to complete.

If your plugin has any required files stored on disk, you should keep them in `~/.config/anklet/plugins/{plugin-name}/`. For example, `github` requires three bash files to prepare the github actions runner in the VMs. They are stored on each host:

```bash
❯ ll ~/.config/anklet/plugins/handlers/github
total 0
lrwxr-xr-x  1 nathanpierce  staff    61B Apr  4 16:02 install-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    62B Apr  4 16:02 register-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    59B Apr  4 16:02 start-runner.bash
```

Each plugin must have a `{name}.go` file with a `Run` function that takes in `context.Context`, etc . See [`handlers/github`](./plugins/handlers/github) plugin for an example.

The `Run` function should be designed to run multiple times in parallel between plugins. This means being aware of global context, locks, etc. It should not rely on any state from the previous runs and fully clean up at the end of each run.
    - Always `return` out of `Run` so the sleep interval in `main.go` can handle the next run properly with new context. Never loop inside of the plugin's `Run` function.
    - Should never panic but instead throw an ERROR and return. The `github` plugin has a go routine that loops and watches for completion of the job, context cancellation, or other signals, then performs cleanup before exiting in all situations. Be aware that context cancellation could prevent cleanup from happening, so use a context specifically for cleanup that is outside of the main context that got cancelled.
    - It's critical that you check for context cancellation after/before important logic that could orphan resources. The sooner you catch it, the better.

## Handling Metrics

Metrics are created for each plugin from the `main.go`. A go routine is created when the first plugin is loaded and continues to run and update the DB with the most recent metrics for each plugin.

Metrics for each plugin in a config are grouped into a DB key with the name of the first plugin in the config. This allows aggregation to more efficiently get the metrics for all plugins.

In your plugin code, you can use functions from metricsData to update in specific situatons. For example a failure:

```go
				case "failure":
					metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
					err = metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
						PluginBase: &metrics.PluginBase{
							Name: pluginConfig.Name,
						},
						LastFailedRun:       time.Now(),
						LastFailedRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
					})
```

Or a job being picked up:

```go
logging.Info(pluginCtx, "handling anka workflow run job")
err = metricsData.SetStatus(pluginCtx, "in_progress")
if err != nil {
  logging.Error(pluginCtx, "error setting plugin status", "err", err)
}
```

See [`internal/metrics/metrics.go`](./internal/metrics/metrics.go) for the full list of functions.

## Testing

Tests are located in the `tests/` directory and are organized into two categories:

### CLI Tests

CLI tests validate Anklet's startup behavior, configuration parsing, and error handling. Run all CLI tests with:

```bash
./tests/cli-tests/run.bash
```

Or run a specific test:

```bash
./tests/cli-tests/run.bash start-stop
```

**Available CLI tests:**

| Test | Description |
|------|-------------|
| `empty` | Validates error handling for empty config files |
| `no-log-directory` | Validates error when log directory doesn't exist |
| `no-plugins` | Validates graceful startup with no plugins configured |
| `no-plugin-name` | Validates error when plugin name is missing |
| `non-existent-plugin` | Validates error for unknown plugin types |
| `no-db` | Validates error handling when database is unavailable |
| `capacity` | Validates VM capacity checking |
| `start-stop` | Validates clean startup and shutdown cycle |

### Plugin Tests

Plugin tests are end-to-end integration tests that validate receiver and handler plugins against real CI platforms. Tests are located in `tests/plugins/{plugin-name}/`. They run on Veertu infrastructure and require the Veertu team to run manually.

Each test directory contains:

- **`manifest.yaml`** - Defines the test environment including required hosts, configurations, and startup scripts
- **`test.bash`** - The test script that runs assertions against workflow runs and logs
- **`*.yaml`** - Host-specific Anklet configuration files

You can find an example test in [tests/plugins/github/1-test-success](./tests/plugins/github/1-test-success).

#### Test Helper Functions

Tests have access to helper functions for common operations. See [`tests/plugins/AVAILABLE_TEST_FUNCTIONS.md`](./tests/plugins/AVAILABLE_TEST_FUNCTIONS.md) for the complete reference.

#### Example Test

[Example test for github plugin you can review](./tests/plugins/github/1-test-success).

### Unit Tests

Go unit tests can be run with:

```bash
make go.test
```
