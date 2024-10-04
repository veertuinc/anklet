# ANKLET

Inspired by our customer requirements, **Anklet** is a solution created to meet the specific needs of our users who cannot use [the existing solution for github actions](https://docs.veertu.com/anka/plugins-and-integrations/controller-+-registry/github-actions/) to run on-demand and ephemeral [Anka macOS VMs](https://docs.veertu.com/anka/what-is-anka/). It does not rely on the [Anka Build Cloud Controller](https://docs.veertu.com/anka/what-is-anka/#controller-less-registry-only).

Here are the requirements:

1. Each team and repository should not have knowledge of the Controller URL, potential auth methods, Anka Node Groups, etc. These are all things that had to be set in the job yaml for [the existing solution for github actions](https://docs.veertu.com/anka/plugins-and-integrations/controller-+-registry/github-actions/). This should be abstracted away for security and simplicity of use.
2. Their workflow files cannot have multiple stages (start -> the actual job that runs in the VM -> a cleanup step) just to run a single Anka VM
3. They don't want the job to be responsible for cleaning up the VM + registered runner either.

While these reasons are specific to Github Actions, they apply to many other CI platforms too. 

Anklet will have a configuration and run custom plugins (written by us or the community) which handle all of the logic necessary to watch/listen for jobs in the specific CI platform. The plugins determine what logic happens host-side to prepare a macOS VM and optionally register it to the CI platform for use. We'll talk more about that below. At the time of writing this, plugins are not idependent, but will eventually be separated.

Note: It does not run as root, and will use the current user's space/environment to run VMs. Our Controller will run under `sudo/root`, but we *do not require* that for anklet.

### How does it really work?

1. Anklet loads the configuration from the `~/.config/anklet/config.yml` file on the same host. The configuration defines the plugins that will be started.
    - Each plugin in the config specifies a plugin to load and use, the database (if there is one), and any other specific configuration for that plugin.
1. Plugins run in parallel, but have separate internal context to avoid collisions.
1. It supports loading in a database (currently `redis`) to manage state across all of your hosts.
    - The `github` plugin, and likely others, rely on this to prevent race conditions with picking up jobs.
    - It is `disabled: true` by default to make anklet more lightweight.
1. Logs are in JSON format and are written to `./anklet.log` (unless otherwise specified). Here is an example of the log structure:
    ```JSON
    {
      "time": "2024-04-03T17:10:08.726639-04:00",
      "level": "INFO",
      "msg": "handling anka workflow run job",
      "ankletVersion": "dev",
      "pluginName": "RUNNER1",
      "plugin": "github",
      "repo": "anklet",
      "owner": "veertuinc",
      "workflowName": "t1-without-tag",
      "workflowRunName": "t1-without-tag",
      "workflowRunId": 8544945071,
      "workflowJobId": 23414111000,
      "workflowJobName": "testJob",
      "uniqueId": "1",
      "ankaTemplate": "d792c6f6-198c-470f-9526-9c998efe7ab4",
      "ankaTemplateTag": "(using latest)",
      "jobURL": "https://github.com/veertuinc/anklet/actions/runs/8544945071/job/23414111000",
      "uniqueRunKey": "8544945071:1"
    }
    ```
    - All critical errors your Ops team needs to watch for are level `ERROR`.

---

### How does it manage VM Templates on the host?

Anklet handles VM [Templates/Tags](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/creating-vms/#vm-templates) the best it can using the Anka CLI.
- If the VM Template or Tag does not exist, Anklet will pull it from the Registry using the `default` configured registry under `anka registry list-repos`. You can also set the `registry_url` in the `config.yml` to use a different registry.
    - Two consecutive pulls cannot happen on the same host or else the data may become corrupt. If a second job is picked up that requires a pull, it will send it back to the queue so another host can handle it.
- If the Template *AND* Tag already exist, it does *not* issue a pull from the Registry (which therefore doesn't require maintaining a Registry at all; useful for users who use `anka export/import`). Important: You must define the tag, or else it will attempt to use "latest" and forcefully issue a pull.

---

## Setup Guide

We're going to use github as an example, but the process is similar for other plugins.

With the github plugin, there is a Receiver Plugin and a Handler Plugin.

- The github Receiver Plugin is a web server that listens for webhooks github sends and then places the events in the database/queue. It can run on mac and linux.
- The github Handler Plugin is responsible for pulling a job from the database/queue, preparing a macOS VM, and registering it to the repo's action runners so it can execute the job inside. It can run on mac as it needs access to the Anka CLI.

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
    plugins:
      # GITHUB RECEIVER
      - name: GITHUB_WEBHOOK_RECEIVER
        plugin: github_receiver
        hook_id: 489747753
        port: 54321 # port that's open to the internet so github can post to it
        secret: 00000000
        private_key: /Users/nathanpierce/veertuinc-anklet.2024-07-19.private-key.pem
        app_id: 949431
        installation_id: 52970581
        repo: anklet
        owner: veertuinc
        database:
          enabled: true
          url: localhost
          port: 6379
          user: ""
          password: ""
          database: 0
      # GITHUB HANDLERS
      - name: RUNNER1
          plugin: github
          private_key: /Users/nathanpierce/veertuinc-anklet.2024-07-19.private-key.pem
          app_id: 949431
          installation_id: 52970581
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
    > Note: You can only ever run two VMs per host per the Apple macOS SLA. While you can specify more than two plugins, only two will ever be running a VM at one time. `sleep_interval` can be used to control the frequency/priority of a plugin and increase the odds that a job will be picked up.
3. Run the binary on the host that has the [Anka CLI installed](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/installing-the-anka-virtualization-package/) (Anka is not needed if just running an Anklet Receiver).
    - `tail -fF /Users/myUser/Library/Logs/anklet.log` to see the logs. You can run `anklet` with `LOG_LEVEL=DEBUG` to see more verbose output.
3. To stop, send an interrupt or ctrl+c. It will attempt a graceful shut down of plugins, sending unfinished jobs back to the queue or waiting until the job is done to prevent orphans.

It is also possible to use ENVs for several of the items in the config. They override anything set in the yml. Here is a list of ENVs that you can use:

| ENV | Description |
| --- | --- |
| ANKLET_WORK_DIR | Absolute path to work directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_PID_FILE_DIR | Absolute path to pid file directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_LOG_FILE_DIR | Absolute path to log file directory for anklet (ex: /Users/myUser/Library/Logs/) (defaults to `./`) |

For error handling, see the [github plugin README](./plugins/handlers/github/README.md).

### Database Setup

At the moment we support `redis` 7.x for the database. For testing, it can be installed on macOS using homebrew. We recommend choosing one of your Anklet hosts to run the database on and pointing all other hosts to it in their config.

```bash
brew install redis
sudo sysctl kern.ipc.somaxconn=511 # you can also add to /etc/sysctl.conf and reboot
brew services start redis # use sudo on ec2
tail -fF /opt/homebrew/var/log/redis.log
```

For production, we recommend running a [redis cluster](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/) on infrastructure that is separate from your Anklet hosts and has guaranteed uptime.

### Plugin Setup and Usage Guides

#### Github Actions

- [**`Webhook Receiver Plugin`**](./plugins/receivers/github/README.md)
- [**`Anka VM Handler Plugin`**](./plugins/handlers/github/README.md)

---

### Metrics

Metrics for monitoring are available at `http://127.0.0.1:8080/metrics?format=json` or `http://127.0.0.1:8080/metrics?format=prometheus`. These instructions apply to handler and receiver plugins, but receivers can differ slightly in what metrics are available. Be sure to check the specific plugin documentation for more information and examples.

- You can change the port in the `config.yml` under `metrics`, like so:

    ```yaml
    metrics:
      port: 8080
    ```

#### Key Names and Descriptions

| Key | Description | 
| ------ | ----------- |
| total_running_vms | Total number of running VMs |
| total_successful_runs_since_start | Total number of successful runs since start |
| total_failed_runs_since_start | Total number of failed runs since start |
| plugin_name | Name of the plugin |
| plugin_plugin_name | Name of the plugin |
| plugin_owner_name | Name of the owner |
| plugin_repo_name | Name of the repo |
| plugin_status | Status of the plugin (idle, running, limit_paused, stopped) |
| plugin_last_successful_run_job_url | Last successful run job url of the plugin |
| plugin_last_failed_run_job_url | Last failed run job url of the plugin |
| plugin_last_successful_run | Timestamp of last successful run of the plugin (RFC3339) |
| plugin_last_failed_run | Timestamp of last failed run of the plugin (RFC3339) |
| plugin_status_since | Timestamp of when the plugin was last started (RFC3339) |
| host_cpu_count | Total CPU count of the host |
| host_cpu_used_count | Total in use CPU count of the host |
| host_cpu_usage_percentage | CPU usage percentage of the host |
| host_memory_total_bytes | Total memory of the host (bytes) |
| host_memory_used_bytes | Used memory of the host (bytes) |
| host_memory_available_bytes | Available memory of the host (bytes) |
| host_memory_usage_percentage | Memory usage percentage of the host |
| host_disk_total_bytes | Total disk space of the host (bytes) |
| host_disk_used_bytes | Used disk space of the host (bytes) |
| host_disk_available_bytes | Available disk space of the host (bytes) |
| host_disk_usage_percentage | Disk usage percentage of the host |

#### JSON

```json
{
  "total_running_vms": 0,
  "total_successful_runs_since_start": 2,
  "total_failed_runs_since_start": 2,
  "host_cpu_count": 12,
  "host_cpu_used_count": 0,
  "host_cpu_usage_percentage": 5.572289151578012,
  "host_memory_total_bytes": 38654705664,
  "host_memory_used_bytes": 23025205248,
  "host_memory_available_bytes": 15629500416,
  "host_memory_usage_percentage": 59.56637064615885,
  "host_disk_total_bytes": 994662584320,
  "host_disk_used_bytes": 459045515264,
  "host_disk_available_bytes": 535617069056,
  "host_disk_usage_percentage": 46.150877945994715,
  "plugins": [
    {
      "name": "RUNNER2",
      "plugin_name": "github",
      "repo_name": "anklet",
      "owner_name": "veertuinc",
      "status": "idle",
      "last_successful_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/9180172013/job/25243983121",
      "last_failed_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/9180170811/job/25243979917",
      "last_successful_run": "2024-05-21T14:16:06.300971-05:00",
      "last_failed_run": "2024-05-21T14:15:10.994464-05:00",
      "status_since": "2024-05-21T14:16:06.300971-05:00"
    },
    {
      "name": "RUNNER1",
      "plugin_name": "github",
      "repo_name": "anklet",
      "owner_name": "veertuinc",
      "status": "idle",
      "last_successful_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/9180172546/job/25243984537",
      "last_failed_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/9180171228/job/25243980930",
      "last_successful_run": "2024-05-21T14:16:35.532016-05:00",
      "last_failed_run": "2024-05-21T14:15:45.930051-05:00",
      "status_since": "2024-05-21T14:16:35.532016-05:00"
    }
  ]
}
```

#### Prometheus

```
total_running_vms 0
total_successful_runs_since_start 2
total_failed_runs_since_start 2
plugin_status{plugin_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet} idle
plugin_last_successful_run{plugin_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180172013/job/25243983121} 2024-05-21T14:16:06-05:00
plugin_last_failed_run{plugin_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180170811/job/25243979917} 2024-05-21T14:15:10-05:00
plugin_status_since{plugin_name=RUNNER2,plugin=github,owner=veertuinc,repo=anklet} 2024-05-21T14:16:06-05:00
plugin_status{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet} idle
plugin_last_successful_run{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180172546/job/25243984537} 2024-05-21T14:16:35-05:00
plugin_last_failed_run{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=https://github.com/veertuinc/anklet/actions/runs/9180171228/job/25243980930} 2024-05-21T14:15:45-05:00
plugin_status_since{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet} 2024-05-21T14:16:35-05:00
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

In order to enable the aggregator, you will want to run an Anklet with the `aggregator` flag set to `true`. **You want to run this separate from any plugins.** This will start an Anklet Aggregator service that will collect metrics from all Anklets defined in `metrics_urls` and make them available at `http://{aggregator_url}:{port}/metrics?format=json` or `http://{aggregator_url}:{port}/metrics?format=prometheus`. Here is an example config:

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

Finally, here are the example responses of each format:

#### JSON

```json
{
  "http://127.0.0.1:8080/metrics": {
    "total_running_vms": 0,
    "total_successful_runs_since_start": 0,
    "total_failed_runs_since_start": 0,
    "host_cpu_count": 12,
    "host_cpu_used_count": 1,
    "host_cpu_usage_percentage": 11.765251850227692,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 27379499008,
    "host_memory_available_bytes": 11275206656,
    "host_memory_usage_percentage": 70.83095974392361,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 540768464896,
    "host_disk_available_bytes": 453894119424,
    "host_disk_usage_percentage": 54.367025906146424,
    "plugins": [
      {
        "name": "RUNNER1",
        "plugin_name": "github",
        "repo_name": "anklet",
        "owner_name": "veertuinc",
        "status": "idle",
        "last_successful_run_job_url": "",
        "last_failed_run_job_url": "",
        "last_successful_run": "0001-01-01T00:00:00Z",
        "last_failed_run": "0001-01-01T00:00:00Z",
        "status_since": "2024-06-11T13:57:43.332009-05:00"
      }
    ]
  },
  "http://192.168.1.183:8080/metrics": {
    "total_running_vms": 0,
    "total_successful_runs_since_start": 0,
    "total_failed_runs_since_start": 0,
    "host_cpu_count": 8,
    "host_cpu_used_count": 1,
    "host_cpu_usage_percentage": 20.964819937820444,
    "host_memory_total_bytes": 25769803776,
    "host_memory_used_bytes": 18017533952,
    "host_memory_available_bytes": 7752269824,
    "host_memory_usage_percentage": 69.91723378499348,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 629847568384,
    "host_disk_available_bytes": 364815015936,
    "host_disk_usage_percentage": 63.32273660565956,
    "plugins": [
      {
        "name": "RUNNER3",
        "plugin_name": "github",
        "repo_name": "anklet",
        "owner_name": "veertuinc",
        "status": "idle",
        "last_successful_run_job_url": "",
        "last_failed_run_job_url": "",
        "last_successful_run": "0001-01-01T00:00:00Z",
        "last_failed_run": "0001-01-01T00:00:00Z",
        "status_since": "2024-06-11T14:16:42.324542-05:00"
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
plugin_status{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://127.0.0.1:8080/metrics} idle
plugin_last_successful_run{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://127.0.0.1:8080/metrics} 0001-01-01T00:00:00Z
plugin_last_failed_run{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://127.0.0.1:8080/metrics} 0001-01-01T00:00:00Z
plugin_status_since{plugin_name=RUNNER1,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://127.0.0.1:8080/metrics} 2024-06-11T13:57:43-05:00
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
plugin_status{plugin_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://192.168.1.183:8080/metrics} idle
plugin_last_successful_run{plugin_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://192.168.1.183:8080/metrics} 0001-01-01T00:00:00Z
plugin_last_failed_run{plugin_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,job_url=,metricsUrl=http://192.168.1.183:8080/metrics} 0001-01-01T00:00:00Z
plugin_status_since{plugin_name=RUNNER3,plugin=github,owner=veertuinc,repo=anklet,metricsUrl=http://192.168.1.183:8080/metrics} 2024-06-11T14:16:42-05:00
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

## Docker / Containers

Docker images are available at [veertu/anklet](https://hub.docker.com/r/veertu/anklet). [You can find the example docker-compose file in the `docker` directory.](https://github.com/veertuinc/anklet/blob/main/docker/docker-compose.yml)

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
    "file": "/Users/nathanpierce/anklet/plugins/handlers/github/github.go",
    "function": "github.com/veertuinc/anklet/plugins/handlers/github.Run",
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

Plugins are, currently, stored in the `plugins/` directory. They will be moved into external binaries at some point in the future.

#### Guidelines

**Important:** Avoid handling context cancellations in places of the code that will need to be done before the runner exits. This means any VM deletion or database cleanup must be done using functions that do not have context cancellation, allowing them to complete.

If your plugin has any required files stored on disk, you should keep them in `~/.config/anklet/plugins/{plugin-name}/`. For example, `github` requires three bash files to prepare the github actions runner in the VMs. They are stored on each host: 

```bash
❯ ll ~/.config/anklet/plugins/handlers/github
total 0
lrwxr-xr-x  1 nathanpierce  staff    61B Apr  4 16:02 install-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    62B Apr  4 16:02 register-runner.bash
lrwxr-xr-x  1 nathanpierce  staff    59B Apr  4 16:02 start-runner.bash
```

Each plugin must have a `{name}.go` file with a `Run` function that takes in `context.Context` and `logger *slog.Logger`. See `github` plugin for an example.

The `Run` function should be designed to run multiple times in parallel. It should not rely on any state from the previous runs.
    - Always `return` out of `Run` so the sleep interval and main.go can handle the next run properly with new context. Never loop inside of the plugin code.
    - Should never panic but instead throw an ERROR and return. The `github` plugin has a go routine that loops and watches for cancellation, which then performs cleanup before exiting in all situations except for crashes.
    - It's critical that you check for context cancellation after/before important logic that could orphan resources.

### Handling Metrics

Any of the plugins you run are done from within worker context. Each plugin also has a separate plugin context storing its Name, etc. The metrics for the anklet instance is stored in the worker context so they can be accessed by any of the plugins. Plugins should update the metrics for the plugin they are running in at the various phases.

For example, the `github` plugin will update the metrics for the plugin it is running in to be `running`, `pulling`, and `idle` when it is done or has yet to pick up a new job. To do this, it uses `metrics.UpdatePlugin` with the worker and plugin context. See `github` plugin for an example.

But metrics.UpdateService can also update things like `LastSuccess`, and `LastFailure`. See `metrics.UpdateService` for more information.


## Copyright

All rights reserved, Veertu Inc.