# ANKLET

Inspired by our customer requirements, **Anklet** is a ["controller-less"](https://docs.veertu.com/anka/plugins-and-integrations/#controller-less-registry-only) solution created to meet the specific needs of our users who cannot use [the existing solution for github actions](https://docs.veertu.com/anka/plugins-and-integrations/controller-+-registry/github-actions/) to run on-demand and ephemeral [Anka macOS VMs](https://docs.veertu.com/anka/what-is-anka/). Here are some of the reasons why:

1. Each team and repository should not have knowledge of the Controller URL, potential auth methods, Anka Node Groups, etc. These are all things that had to be set in the job yaml for [the existing solution for github actions](https://docs.veertu.com/anka/plugins-and-integrations/controller-+-registry/github-actions/). This should be abstracted away for security and simplicity of use.
2. Their workflow files cannot have multiple stages (start -> the actual job that runs in the VM -> a cleanup step) just to run a single Anka VM
3. They don't want the job to be responsible for cleaning up the VM + registered runner either.

While these reasons are specific to Github Actions, they apply to many other CI platforms too. Users instead want to run a binary on any supported modern Apple Hardware to start a daemon. The daemon will have a configuration and run custom plugins (written by us or the community) which handle all of the logic necessary to watch for jobs in your CI platform. The plugins determine what logic happens host-side to prepare a macOS VM and optionally register it to the CI platform for use. We'll talk more about that below.

Note: It does not run as root, and will use the current user's space/environment to run VMs. Our Controller will run under `sudo/root`, but we *do not require* that for anklet.

### How does it really work?

1. Anklet loads the configuration from the `~/.config/anklet/config.yml` file on the same host. The configuration defines the services that will be started.
    - Each service in the config specifies a plugin to load and use, the database (if there is one), and any other specific configuration for that plugin.
1. Services run in parallel, but have separate internal context to avoid collisions.
1. It supports loading in a database (currently `redis`) to manage state across all of your hosts.
    - The `github` plugin, and likely others, rely on this to prevent race conditions with picking up jobs.
    - It is `disabled: true` by default to make anklet more lightweight by default.
1. To start Anklet, you simple execute the binary with no flags/options. To stop it (and the services running), you can use `-s <stop/drain>` (explained more below) to stop the services.
1. Logs are in JSON format and are written to `./anklet.log` (unless otherwise specified). Here is an example of the log structure:
    ```JSON
    {"time":"2024-04-03T17:10:08.726639-04:00","level":"INFO","msg":"handling anka workflow run job","ankletVersion":"dev","serviceName":"RUNNER1","plugin":"github","repo":"anklet","owner":"veertuinc","workflowName":"t1-without-tag","workflowRunName":"t1-without-tag","workflowRunId":8544945071,"workflowJobId":23414111000,"workflowJobName":"testJob","uniqueId":"1","ankaTemplate":"d792c6f6-198c-470f-9526-9c998efe7ab4","ankaTemplateTag":"(using latest)","jobURL":"https://github.com/veertuinc/anklet/actions/runs/8544945071/job/23414111000","uniqueRunKey":"8544945071:1"}
    ```
    - All critical errors your Ops team needs to watch for are level `ERROR`.


### Resource Expectations

Anklet is fairly lightweight. When running 2 `github` plugin services, we see consistently less than 1 CPU and ~15MB of memory used. This could differ depending on the plugin being used.

---

### How does it manage VM Templates on the host?

Anklet handles VM [Templates/Tags](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/creating-vms/#vm-templates) the best it can using the Anka CLI.
- If the VM Template or Tag does not exist, Anklet will pull it from the Registry using the `default` configured registry under `anka registry list-repos`. You can also set the `registry_url` in the `config.yml` to use a different registry.
    - Two consecutive pulls cannot happen on the same host or else the data may become corrupt. If a second job is picked up that requires a pull, it will send it back to the queue so another host can handle it.
- If the Template *AND* Tag already exist, it does *not* issue a pull from the Registry (which therefore doesn't require maintaining a Registry at all; useful for users who use `anka export/import`). Important: You must define the tag, or else it will attempt to use "latest" and forcefully issue a pull.

---

## Setup Guide

### Anklet Setup

1. Download the binary from the [releases page](https://github.com/veertuinc/anklet/releases).
1. Use the [Plugin Setup and Usage Guides](#plugin-setup-and-usage-guides) to setup the plugin(s) you want to use.
2. Create a `~/.config/anklet/config.yml` file with the following contents and modify any necessary values. We'll use a config for `github`:
    ```yaml
    ---
    work_dir: /tmp/
    pid_file_dir: /tmp/
    log:
        # if file_dir is not set, it will be set to current directory you execute anklet in
        file_dir: /Users/myUser/Library/Logs/
    services:
    - name: RUNNER1
        plugin: github
        token: github_pat_1XXXXX
        registration: repo
        repo: anklet
        owner: veertuinc
        registry_url: http://anka.registry:8089
        sleep_interval: 10 # sleep 10 seconds between checks for new jobs
        database:
            enabled: true
            url: localhost
            port: 6379
            user: ""
            password: ""
            database: 0
    - name: RUNNER2
        plugin: github
        token: github_pat_1XXXXX
        registration: repo
        repo: anklet
        owner: veertuinc
        registry_url: http://anka.registry:8089
        database:
            enabled: true
            url: localhost
            port: 6379
            user: ""
            password: ""
            database: 0

    ```
    > Note: You can only ever run two VMs per host per the Apple macOS SLA. While you can specify more than two services, only two will ever be running a VM at one time. `sleep_interval` can be used to control the frequency/priority of a service and increase the odds that a job will be picked up.
3. Run the daemon by executing `anklet` on the host that has the [Anka CLI installed](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/installing-the-anka-virtualization-package/).
    - `tail -fF /Users/myUser/Library/Logs/anklet.log` to see the logs. You can run `anklet` with `LOG_LEVEL=DEBUG` to see more verbose output.
3. To stop, you have two options:
    - `anklet -s stop` to stop the services semi-gracefully (interrupt the plugin at the next context cancellation definition, and still try to cleanup gracefully). This requires that the plugin has properly defined context cancellation checks.
    - `anklet -s drain` to stop services, but wait for all jobs to finish gracefully.

It is also possible to use ENVs for several of the items in the config. Here is a list of ENVs that you can use:

| ENV | Description |
| --- | --- |
| ANKLET_WORK_DIR | Absolute path to work directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_PID_FILE_DIR | Absolute path to pid file directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_LOG_FILE_DIR | Absolute path to log file directory for anklet (ex: /Users/myUser/Library/Logs/) (defaults to `./`) |
| ANKLET_DATABASE_ENABLED | Whether to enable the database (ex: true) (defaults to `false`) |
| ANKLET_DATABASE_URL | URL of the database (ex: localhost) |
| ANKLET_DATABASE_PORT | Port of the database (ex: 6379) |
| ANKLET_DATABASE_USER | User to use (ex: "") |
| ANKLET_DATABASE_PASSWORD | Password to use (ex: "") |
| ANKLET_DATABASE_DATABASE | Database to use (ex: 0) |

### Database Setup

At the moment we support `redis` 7.x for the database. It can be installed on macOS using homebrew:

```bash
brew install redis
sudo sysctl kern.ipc.somaxconn=511 # you can also add to /etc/sysctl.conf and reboot
brew services start redis # use sudo on ec2
tail -fF /opt/homebrew/var/log/redis.log
```

While you can run it anywhere you want, its likely going to be less latency to host it on a host[s] that are in the same location at anklet. We recommend to choose one of the macs to run it on and point other hosts to it in their config. It's also possible to cluster redis, but we won't cover that in our guides.

### Plugin Setup and Usage Guides

- #### [**`Github Actions`**](./plugins/github/README.md)


---

### Metrics

Metrics for monitoring are available at `http://127.0.0.1:8080/metrics?format=json` or `http://127.0.0.1:8080/metrics?format=prometheus`.

- You can change the port in the `config.yml` under `metrics`, like so:

    ```yaml
    metrics:
      port: 8080
    ```

#### Key Names and Descriptions

| JSON | Prometheus | Description | 
| ------ | ----------- | ----------- |
| TotalRunningVMs | total_running_vms | Total number of running VMs |
| TotalSuccessfulRunsSinceStart | total_successful_runs_since_start | Total number of successful runs since start |
| TotalFailedRunsSinceStart | total_failed_runs_since_start | Total number of failed runs since start |
| Service::Name | service_name | Name of the service |
| Service::PluginName | service_plugin_name | Name of the plugin |
| Service::OwnerName | service_owner_name | Name of the owner |
| Service::RepoName | service_repo_name | Name of the repo |
| Service::Status | service_status | Status of the service (idle, running, limit_paused, stopped) |
| Service::LastSuccessfulRunJobUrl | service_last_successful_run_job_url | Last successful run job url of the service |
| Service::LastFailedRunJobUrl | service_last_failed_run_job_url | Last failed run job url of the service |
| Service::LastSuccessfulRun | service_last_successful_run | Timestamp of last successful run of the service (RFC3339) |
| Service::LastFailedRun | service_last_failed_run | Timestamp of last failed run of the service (RFC3339) |
| Service::StatusRunningSince | service_status_running_since | Timestamp of when the service was last started (RFC3339) |
| HostCPUCount | host_cpu_count | Total CPU count of the host |
| HostCPUUsedCount | host_cpu_used_count | Total in use CPU count of the host |
| HostCPUUsagePercentage | host_cpu_usage_percentage | CPU usage percentage of the host |
| HostMemoryTotalBytes | host_memory_total_bytes | Total memory of the host (bytes) |
| HostMemoryUsedBytes | host_memory_used_bytes | Used memory of the host (bytes) |
| HostMemoryAvailableBytes | host_memory_available_bytes | Available memory of the host (bytes) |
| HostMemoryUsagePercentage | host_memory_usage_percentage | Memory usage percentage of the host |
| HostDiskTotalBytes | host_disk_total_bytes | Total disk space of the host (bytes) |
| HostDiskUsedBytes | host_disk_used_bytes | Used disk space of the host (bytes) |
| HostDiskAvailableBytes | host_disk_available_bytes | Available disk space of the host (bytes) |
| HostDiskUsagePercentage | host_disk_usage_percentage | Disk usage percentage of the host |

#### JSON

```json
{
  "TotalRunningVMs": 0,
  "TotalSuccessfulRunsSinceStart": 2,
  "TotalFailedRunsSinceStart": 2,
  "HostCPUCount": 12,
  "HostCPUUsedCount": 0,
  "HostCPUUsagePercentage": 5.572289151578012,
  "HostMemoryTotal": 38654705664,
  "HostMemoryUsed": 23025205248,
  "HostMemoryAvailable": 15629500416,
  "HostMemoryUsagePercentage": 59.56637064615885,
  "HostDiskTotal": 994662584320,
  "HostDiskUsed": 459045515264,
  "HostDiskAvailable": 535617069056,
  "HostDiskUsagePercentage": 46.150877945994715,
  "Services": [
    {
      "Name": "RUNNER2",
      "PluginName": "github",
      "RepoName": "anklet",
      "OwnerName": "veertuinc",
      "Status": "idle",
      "LastSuccessfulRunJobUrl": "https://github.com/veertuinc/anklet/actions/runs/9180172013/job/25243983121",
      "LastFailedRunJobUrl": "https://github.com/veertuinc/anklet/actions/runs/9180170811/job/25243979917",
      "LastSuccessfulRun": "2024-05-21T14:16:06.300971-05:00",
      "LastFailedRun": "2024-05-21T14:15:10.994464-05:00",
      "StatusRunningSince": "2024-05-21T14:16:06.300971-05:00"
    },
    {
      "Name": "RUNNER1",
      "PluginName": "github",
      "RepoName": "anklet",
      "OwnerName": "veertuinc",
      "Status": "idle",
      "LastSuccessfulRunJobUrl": "https://github.com/veertuinc/anklet/actions/runs/9180172546/job/25243984537",
      "LastFailedRunJobUrl": "https://github.com/veertuinc/anklet/actions/runs/9180171228/job/25243980930",
      "LastSuccessfulRun": "2024-05-21T14:16:35.532016-05:00",
      "LastFailedRun": "2024-05-21T14:15:45.930051-05:00",
      "StatusRunningSince": "2024-05-21T14:16:35.532016-05:00"
    }
  ]
}
```

#### Prometheus

```
total_running_vms 0
total_successful_runs_since_start 2
total_failed_runs_since_start 2
service_status{service_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet} idle
service_last_successful_run{service_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180172013/job/25243983121} 2024-05-21T14:16:06-05:00
service_last_failed_run{service_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180170811/job/25243979917} 2024-05-21T14:15:10-05:00
service_status_running_since{service_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet} 2024-05-21T14:16:06-05:00
service_status{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet} idle
service_last_successful_run{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180172546/job/25243984537} 2024-05-21T14:16:35-05:00
service_last_failed_run{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180171228/job/25243980930} 2024-05-21T14:15:45-05:00
service_status_running_since{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet} 2024-05-21T14:16:35-05:00
host_cpu_count 12
host_cpu_used_count 1
host_cpu_usage_percentage 10.674157
host_memory_total_bytes 38654705664
host_memory_used_bytes 22701359104
host_memory_available_bytes 15953346560
host_memory_usage_percentage 58.728578
host_disk_total_bytes 994662584320
host_disk_used_bytes 459042254848
host_disk_available_bytes 535620329472
host_disk_usage_percentage 46.150550
```

### Metrics Aggregator

In most cases each individual Anklet serving up their own metrics is good enough for your monitoring needs. However, there are situations where you may need to consume them from a single source instead. The Anklet Aggregator service is designed to do just that.

In order to enable the aggregator, you will want to run an Anklet with the `aggregator` flag set to `true`. **You want to run this separate from any services/plugins.** This will start an Anklet Aggregator service that will collect metrics from all Anklets defined in `metrics_urls` and make them available at `http://{aggregator_url}:{port}/metrics?format=json` or `http://{aggregator_url}:{port}/metrics?format=prometheus`. Here is an example config:

```yaml
---
work_dir: /Users/nathanpierce/anklet/
pid_file_dir: /tmp/
log:
  # if file_dir is not set, it will be set to current directory you execute anklet in
  file_dir: /Users/nathanpierce/Library/Logs/
metrics:
  aggregator: true
  metrics_urls:
    - http://192.168.1.201:8080/metrics
    - http://192.168.1.202:8080/metrics
    - http://192.168.1.203:8080/metrics
  port: 8081 # port to serve aggregator on
  sleep_interval: 10 # how often to fetch metrics from each Anklet defined
  database:
    enabled: true
    url: localhost
    port: 6379
    user: ""
    password: ""
    database: 0
```

You can see that this requires a database to be running. The aggregator will store the metrics in Redis so that it can serve them up without delay.

It's possible to use ENVs instead of the yml file. This is useful if you want to running anklet metrics aggregator in kubernetes. Here is a list of ENVs that you can use:

| ENV | Description |
| --- | --- |
| ANKLET_METRICS_AGGREGATOR | Whether to enable the aggregator (ex: true) |
| ANKLET_METRICS_PORT | Port to serve aggregator on (ex: 8081) |
| ANKLET_METRICS_URLS | Comma separated list of metrics urls to aggregate (ex: http://127.0.0.1:8080/metrics,http://192.168.1.202:8080/metrics) |
| ANKLET_METRICS_SLEEP_INTERVAL | How many seconds between fetching metrics from each Anklet url defined |
| ANKLET_METRICS_DATABASE_ENABLED | Whether to enable the database (ex: true) |
| ANKLET_METRICS_DATABASE_URL | URL of the database (ex: localhost) |
| ANKLET_METRICS_DATABASE_PORT | Port of the database (ex: 6379) |
| ANKLET_METRICS_DATABASE_DATABASE | Database to use (ex: 0) |
| ANKLET_METRICS_DATABASE_USER | User to use (ex: "") |
| ANKLET_METRICS_DATABASE_PASSWORD | Password to use (ex: "") |

An example response of each format is as follows:

#### JSON

```json
{
  "http://127.0.0.1:8080/metrics": {
    "TotalRunningVMs": 0,
    "TotalSuccessfulRunsSinceStart": 0,
    "TotalFailedRunsSinceStart": 0,
    "HostCPUCount": 12,
    "HostCPUUsedCount": 1,
    "HostCPUUsagePercentage": 11.765251850227692,
    "HostMemoryTotalBytes": 38654705664,
    "HostMemoryUsedBytes": 27379499008,
    "HostMemoryAvailableBytes": 11275206656,
    "HostMemoryUsagePercentage": 70.83095974392361,
    "HostDiskTotalBytes": 994662584320,
    "HostDiskUsedBytes": 540768464896,
    "HostDiskAvailableBytes": 453894119424,
    "HostDiskUsagePercentage": 54.367025906146424,
    "Services": [
      {
        "Name": "RUNNER1",
        "PluginName": "github",
        "RepoName": "anklet",
        "OwnerName": "veertuinc",
        "Status": "idle",
        "LastSuccessfulRunJobUrl": "",
        "LastFailedRunJobUrl": "",
        "LastSuccessfulRun": "0001-01-01T00:00:00Z",
        "LastFailedRun": "0001-01-01T00:00:00Z",
        "StatusRunningSince": "2024-06-11T13:57:43.332009-05:00"
      }
    ]
  },
  "http://192.168.1.183:8080/metrics": {
    "TotalRunningVMs": 0,
    "TotalSuccessfulRunsSinceStart": 0,
    "TotalFailedRunsSinceStart": 0,
    "HostCPUCount": 8,
    "HostCPUUsedCount": 1,
    "HostCPUUsagePercentage": 20.964819937820444,
    "HostMemoryTotalBytes": 25769803776,
    "HostMemoryUsedBytes": 18017533952,
    "HostMemoryAvailableBytes": 7752269824,
    "HostMemoryUsagePercentage": 69.91723378499348,
    "HostDiskTotalBytes": 994662584320,
    "HostDiskUsedBytes": 629847568384,
    "HostDiskAvailableBytes": 364815015936,
    "HostDiskUsagePercentage": 63.32273660565956,
    "Services": [
      {
        "Name": "RUNNER3",
        "PluginName": "github",
        "RepoName": "anklet",
        "OwnerName": "veertuinc",
        "Status": "idle",
        "LastSuccessfulRunJobUrl": "",
        "LastFailedRunJobUrl": "",
        "LastSuccessfulRun": "0001-01-01T00:00:00Z",
        "LastFailedRun": "0001-01-01T00:00:00Z",
        "StatusRunningSince": "2024-06-11T14:16:42.324542-05:00"
      }
    ]
  }
}
```

#### Prometheus

This will be a text list, differentiating metrics by `metricsUrl`.

```
total_running_vms{metricsUrl=http://127.0.0.1:8080/metrics} 0
total_successful_runs_since_start{metricsUrl=http://127.0.0.1:8080/metrics} 0
total_failed_runs_since_start{metricsUrl=http://127.0.0.1:8080/metrics} 0
service_status{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://127.0.0.1:8080/metrics} idle
service_last_successful_run{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://127.0.0.1:8080/metrics} 0001-01-01T00:00:00Z
service_last_failed_run{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://127.0.0.1:8080/metrics} 0001-01-01T00:00:00Z
service_status_running_since{service_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://127.0.0.1:8080/metrics} 2024-06-11T13:57:43-05:00
host_cpu_count{metricsUrl=http://127.0.0.1:8080/metrics} 12
host_cpu_used_count{metricsUrl=http://127.0.0.1:8080/metrics} 0
host_cpu_usage_percentage{metricsUrl=http://127.0.0.1:8080/metrics} 7.300310
host_memory_total_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 38654705664
host_memory_used_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 27103789056
host_memory_available_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 11550916608
host_memory_usage_percentage{metricsUrl=http://127.0.0.1:8080/metrics} 70.117696
host_disk_total_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 994662584320
host_disk_used_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 540769202176
host_disk_available_bytes{metricsUrl=http://127.0.0.1:8080/metrics} 453893382144
host_disk_usage_percentage{metricsUrl=http://127.0.0.1:8080/metrics} 54.367100
total_running_vms{metricsUrl=http://192.168.1.183:8080/metrics} 0
total_successful_runs_since_start{metricsUrl=http://192.168.1.183:8080/metrics} 0
total_failed_runs_since_start{metricsUrl=http://192.168.1.183:8080/metrics} 0
service_status{service_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://192.168.1.183:8080/metrics} idle
service_last_successful_run{service_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://192.168.1.183:8080/metrics} 0001-01-01T00:00:00Z
service_last_failed_run{service_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://192.168.1.183:8080/metrics} 0001-01-01T00:00:00Z
service_status_running_since{service_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://192.168.1.183:8080/metrics} 2024-06-11T14:16:42-05:00
host_cpu_count{metricsUrl=http://192.168.1.183:8080/metrics} 8
host_cpu_used_count{metricsUrl=http://192.168.1.183:8080/metrics} 1
host_cpu_usage_percentage{metricsUrl=http://192.168.1.183:8080/metrics} 20.760717
host_memory_total_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 25769803776
host_memory_used_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 17975410688
host_memory_available_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 7794393088
host_memory_usage_percentage{metricsUrl=http://192.168.1.183:8080/metrics} 69.753774
host_disk_total_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 994662584320
host_disk_used_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 629849382912
host_disk_available_bytes{metricsUrl=http://192.168.1.183:8080/metrics} 364813201408
host_disk_usage_percentage{metricsUrl=http://192.168.1.183:8080/metrics} 63.322919
```


---

## Development

### Prepare your environment for development:

```bash
brew install go
go mod tidy
LOG_LEVEL=dev go run main.go
tail -fF ~/Library/Logs/anklet.log
```

The `dev` LOG_LEVEL has colored output with text + pretty printed JSON for easier debugging. Here is an example:

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
    "file": "/Users/nathanpierce/anklet/plugins/github/github.go",
    "function": "github.com/veertuinc/anklet/plugins/github.Run",
    "line": 408
  },
  "uniqueId": "1",
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
- Run each service only once with `LOG_LEVEL=dev go run -ldflags "-X main.runOnce=true" main.go`

### Plugins

Plugins are, currently, stored in the `plugins/` directory.

#### Guidelines

**Important:** Avoid handling context cancellations in places of the code that will need to be done before the runner exits. This means any VM deletion or database cleanup must be done using functions that do not have context cancellation watches.

If your plugin has any required files stored on disk, you should keep them in `~/.config/anklet/plugins/{plugin-name}/`. For example, `github` requires three bash files to prepare the github actions runner in the VMs. They are stored on each host: 

```bash
❯ ll ~/.config/anklet/plugins/github
total 0
lrwxr-xr-x  1 nathanpierce  staff    61B Apr  4 16:02 install-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    62B Apr  4 16:02 register-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    59B Apr  4 16:02 start-runner.bash
```

Each plugin must have a `{name}.go` file with a `Run` function that takes in `context.Context` and `logger *slog.Logger`. See `github` plugin for an example.

The `Run` function should be designed to run multiple times in parallel. It should not rely on any state from the previous runs.
    - Always `return` out of `Run` so the sleep interval and main.go can handle the next run properly with new context. Never loop inside of the plugin code.
    - Should never panic but instead throw an ERROR and return.
    - It's critical that you check for context cancellation before important logic that could orphan resources.

### Handling Metrics

Any of the services you run are done from within worker context. Each service also has a separate service context storing its Name, etc. The metrics for the anklet instance is stored in the worker context so they can be accessed by any of the services. Plugins should update the metrics for the service they are running in at the various phases.

For example, the `github` plugin will update the metrics for the service it is running in to be `running`, `pulling`, and `idle` when it is done or has yet to pick up a new job. To do this, it uses `metrics.UpdateService` with the worker and service context. See `github` plugin for an example.

But metrics.UpdateService can also update things like `LastSuccess`, and `LastFailure`. See `metrics.UpdateService` for more information.
