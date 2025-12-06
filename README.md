# ANKLET

Inspired by our customer requirements, **Anklet** is a solution created to meet the specific on-demand and ephemeral macoS VM automation needs of our users, no matter what CI platform or tool they use.

## High Level Overview

Veertu's Anklet is a tool that runs custom [plugins](./plugins) to communicate with your CI platform/tools and the Anka CLI running on macOS hosts.

Depending on the plugin, generally you'll create a cluster with:

1. One linux container running Anklet + a **[Receiver](./plugins/receivers)** plugin.
2. One or more macOS hosts that have the Anka CLI installed and are running Anklet + a **[Handler](./plugins/handlers)** plugin. Note: Each Anklet can run multiple plugins in parallel on the same host. This means you can service multiple CI platforms/tools on the same macOS hosts or just run multiple VMs on the same host for the same tool.

The **Receiver** is responsible for listening for events from your CI platform/tool and placing them in a queue (redis DB).

The **Handler** is responsible for pulling jobs from the queue, preparing a macOS VM, and registering the VM to the CI platform/tool so it can execute the job inside.

Optionally, you can run a **Metrics Aggregator** to collect metrics from the DB and serve them up in a single endpoint.

## Why Anklet?

Here are a few needs our customers expressed so you can understand the motivation for Anklet:

1. Each team, repository, and CI platform should not have knowledge of the Anka Build Cloud Controller URL, potential auth methods, etc. These are all things that had to be set in the job yaml for our older, for example, Github Actions solution. This should be abstracted away for security and simplicity of use.
2. Teams should have minimal definition necessary and just expect their jobs to run inside of a macOS VM. Developer CI workflow files cannot have multiple stages (start -> the actual job that runs in the VM -> a cleanup step) just to run a single Anka VM... That's just too much overhead to ask developers to manage! Instead, something should spin up the VM behind the scenes, register the runner, and then execute the job inside the VM directly.
3. In the same vein, the job that runs in the CI tool should not be responsible for cleaning up the VM or registered runner either. Something else should watch the status of the job and clean up the VM when it's complete. Developers don't want to be responsible for this.

While these reasons are specific to Github Actions, they apply to many other CI platforms too.

---

## How does it really work?

Anklet will have a configuration that defines custom plugins (written by us and/or the community) which handle all of the logic necessary to watch/listen for jobs in the specific CI platform. The plugins determine what logic happens host-side, and for example, prepare a macOS VM + register it to the CI platform for use. We'll talk more about that below. At the time of writing this, plugins are part of Anklet as a monorepo, but will eventually be separated.

1. Anklet loads the configuration from the `~/.config/anklet/config.yml` file on the same host. The configuration defines the plugins that will be started. [Example below.](#anklet-setup)
    - Each `plugins:` list item in the config specifies a plugin to load and use, potentially a database, and any other specific configuration for that plugin.
1. Plugins run in parallel, but have separate internal context to avoid collisions.
1. It supports loading in a database (currently `redis`) to manage state across all of your hosts.
    - The `github` plugin, and likely others, rely on this to prevent race conditions with picking up jobs.
    - It is `disabled: true` by default to make anklet more lightweight should you not need a database for your plugins.
1. Logs are in JSON format and are written to STDOUT (unless otherwise specified).
    ```
    ❯ ./anklet
    {"time":"2025-01-06T08:57:53.043991-06:00","level":"ERROR","msg":"unable to load config.yml (is it in the work_dir, or are you using an absolute path?)","error":"open /Users/nathanpierce/.config/anklet/config.yml: no such file or directory"}
    ```

Here is an example of the log structure (made pretty; normally it's without newlines):

  ```JSON
{
  "ankaTemplate": "d792c6f6-198c-470f-9526-9c998efe7ab4",
  "ankaTemplateTag": "vanilla+port-forward-22+brew-git",
  "ankletVersion": "dev",
  "jobURL": "https://github.com/veertuinc/anklet/actions/runs/12774640532/job/35609194339",
  "owner": "veertuinc",
  "plugin": "github",
  "pluginName": "RUNNER1",
  "repo": "anklet",
  "workflowJobID": 35609194339,
  "workflowJobName": "t1-with-tag-1-matrix-nodes-2 (2)",
  "workflowJobRunID": 12774640532,
  "workflowName": "t1-with-tag-1-matrix-nodes-2"
}
  ```

 Important: All critical errors your Ops team needs to watch for are level `ERROR`.

---

### How does it manage VM Templates on the host?

If your plugin uses the Anka CLI to create VMs, Anklet handles VM [Templates/Tags](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/creating-vms/#vm-templates) the best it can.

1. If the VM Template or Tag does not exist, Anklet will pull it from the Registry using the `default` configured registry under `anka registry list-repos`. You can also set the `registry_url` in the `config.yml` to use a different registry.
    - Two consecutive pulls cannot happen on the same host or else the data may become corrupt. If a second job is picked up that requires a pull, it will send it back to the queue so another host can handle it.
2. If the Template *AND* Tag already exist, it does *not* issue a pull from the Registry (which therefore doesn't require maintaining a Registry at all; useful for users who use `anka export/import`). Important: You must define the tag, or else it will attempt to use "latest" and forcefully issue a pull.

---

## Setup Guide

We're going to use Github Actions as an example, but the process is similar for other plugins.

With the Github Actions plugin, there is a **Receiver** Plugin and a **Handler** Plugin.

- The Github Actions **Receiver** Plugin is a web server that listens for webhooks Github sends and then places the events in the database/queue. It can run on mac and linux.
- The Github Actions **Handler** Plugin is responsible for pulling a job from the database/queue, preparing a macOS VM, and registering it to the repo's action runners so it can execute the job inside. It can only run on macOS as it needs access to the Anka CLI.

### Anklet Setup

1. Download the binary from the [releases page](https://github.com/veertuinc/anklet/releases).
1. Create a `~/.config/anklet/config.yml` file with the following contents. We'll configure the plugins next.

```yaml
---
work_dir: /tmp/
pid_file_dir: /tmp/
# If you want to use the same database for all your plugins, you can set them below:
# global_database_url: localhost
# global_database_port: 6379
# global_database_user: ""
# global_database_password: ""
# global_database_database: 0
# global_private_key: /Users/{YOUR USER HERE}/.private-key.pem # If you use the same key for all your plugins, you can set it here.
# plugins_path: ~/.config/anklet/plugins/ # This sets the location where scripts used by plugins are stored; we don't recommend changing this.
plugins:
```
1. [Set up the Receiver Plugin.](./plugins/receivers/github/README.md)
1. [Set up the Handler Plugin.](./plugins/handlers/github/README.md)
1. Run the binary on the host that has the [Anka CLI installed](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/installing-the-anka-virtualization-package/) (Anka is not needed if just running an Anklet Receiver).
    - You can run `anklet` with `LOG_LEVEL=DEBUG` to see more verbose JSON output, or `LOG_LEVEL=DEBUG-PRETTY` for colored pretty-printed output.
1. To stop, send an interrupt signal or ctrl+c. It will attempt a graceful shut down of plugins, sending unfinished jobs back to the queue or waiting until the job is done to prevent orphans.

It is also possible to use ENVs for several of the items in the config. They override anything set in the yml. Here is a list of ENVs that you can use:

| ENV | Description |
| --- | --- |
| ANKLET_WORK_DIR | Absolute path to work directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_PID_FILE_DIR | Absolute path to pid file directory for anklet (ex: /tmp/) (defaults to `./`) |
| ANKLET_LOG_FILE_DIR | Absolute path to log file directory for anklet (ex: /Users/myUser/Library/Logs/) (defaults to `./`) |
| ANKLET_PLUGINS_PATH | Absolute path to plugins directory for anklet (ex: /Users/myUser/anklet/plugins/) (defaults to `~/.config/anklet/plugins/`) |
| ANKLET_GLOBAL_DATABASE_URL | URL of the database (ex: localhost) |
| ANKLET_GLOBAL_DATABASE_PORT | Port of the database (ex: 6379) |
| ANKLET_GLOBAL_DATABASE_USER | User to use (ex: "") |
| ANKLET_GLOBAL_DATABASE_PASSWORD | Password to use (ex: "") |
| ANKLET_GLOBAL_DATABASE_DATABASE | Database to use (ex: 0) |
| ANKLET_GLOBAL_DATABASE_MAX_RETRIES | Maximum number of retries for database operations (ex: 5) |
| ANKLET_GLOBAL_DATABASE_RETRY_DELAY | Delay between retries for database operations (ex: 1000) |
| ANKLET_GLOBAL_DATABASE_RETRY_BACKOFF_FACTOR | Backoff factor for database operations (ex: 2.0) |
| ANKLET_GLOBAL_DATABASE_CLUSTER_MODE | Whether to use Redis cluster mode (ex: true) |
| ANKLET_GLOBAL_DATABASE_TLS_ENABLED | Whether to use TLS for the database connection (ex: true) |
| ANKLET_GLOBAL_DATABASE_TLS_INSECURE | Whether to skip TLS certificate verification for the database connection (ex: true) |
| ANKLET_GLOBAL_PRIVATE_KEY | Absolute path to private key for anklet (ex: /Users/myUser/.private-key.pem) |
| ANKLET_GLOBAL_RECEIVER_SECRET | Secret to use for receiver plugin (ex: "my-secret") |
| ANKLET_GLOBAL_TEMPLATE_DISK_BUFFER | Disk buffer (how much disk space to leave free on the host) percentage for templates (ex: 10.0 for 10%) |


### Database Setup

At the moment we support `redis` 7.x for the database. For testing, it can be installed on macOS using homebrew. We recommend choosing one of your Anklet hosts to run the database on and pointing all other hosts to it in their config.

```bash
brew install redis
sudo sysctl kern.ipc.somaxconn=511 # you can also add to /etc/sysctl.conf and reboot
brew services start redis # use sudo on ec2
tail -fF /opt/homebrew/var/log/redis.log
```

For production, we recommend running a [redis cluster](https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/) on infrastructure that is separate from your Anklet hosts and has guaranteed uptime.

Your config.yml file must define the database in one of the following ways:
- Using the `database` variables (under each plugin).
- Using the `global_database_*` variables (applies to and overrides the `database` variables under each plugin).
- Using the ENVs: `ANKLET_GLOBAL_DATABASE_URL`, `ANKLET_GLOBAL_DATABASE_PORT`, `ANKLET_GLOBAL_DATABASE_USER`, `ANKLET_GLOBAL_DATABASE_PASSWORD`, `ANKLET_GLOBAL_DATABASE_DATABASE`, `ANKLET_GLOBAL_DATABASE_CLUSTER_MODE`, `ANKLET_GLOBAL_DATABASE_TLS_ENABLED`, `ANKLET_GLOBAL_DATABASE_TLS_INSECURE`.

### Plugin Setup and Usage Guides

You can control the location plugins are stored on the host by setting the `plugins_path` in the `config.yml` file. If not set, it will default to `~/.config/anklet/plugins/`. 

**NOTE: Plugin names MUST be unique across all hosts.**

#### Github Actions

- [**`Webhook Receiver Plugin`**](./plugins/receivers/github/README.md)
- [**`Workflow Run Job Handler Plugin`**](./plugins/handlers/github/README.md)

#### Docker / Containers

We do not provide any Docker images or kubernetes manifests for Anklet. However, you can find an example docker-compose and Dockerfile in the [`docker`](./docker) directory.

#### MacOS Daemon

You can find how to automate the installation of Anklet and run a PLIST [here](https://github.com/veertuinc/aws-ec2-mac-amis/blob/main/scripts/anklet-install.bash).

---

### Metrics

Metrics for monitoring are available at `http://127.0.0.1:8080/metrics?format=prometheus`. This applies to both handler and receiver plugins, but receivers can differ slightly in what metrics are available. Be sure to check the specific plugin documentation for more information and examples.

Note: If port 8080 is already in use, Anklet will automatically increment the port by 1 until it finds an open port.

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
| total_canceled_runs_since_start | Total number of canceled runs since start |
| plugin_name | Name of the plugin |
| plugin_plugin_name | Name of the plugin |
| plugin_owner_name | Name of the owner |
| plugin_repo_name | Name of the repo |
| plugin_status | Status of the plugin (can change depending on the plugin; see specific plugin documentation for more information) |
| plugin_last_successful_run_job_url | Last successful run job url of the plugin |
| plugin_last_failed_run_job_url | Last failed run job url of the plugin |
| plugin_last_successful_run | Timestamp of last successful run of the plugin (RFC3339) |
| plugin_last_failed_run | Timestamp of last failed run of the plugin (RFC3339) |
| plugin_status_since | Timestamp of when the plugin was last started (RFC3339) |
| plugin_total_ran_vms | Total number of VMs ran by the plugin |
| plugin_total_successful_runs_since_start | Total number of successful runs since start |
| plugin_total_failed_runs_since_start | Total number of failed runs since start |
| plugin_total_canceled_runs_since_start | Total number of canceled runs since start |
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

<!-- #### JSON

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
``` -->

#### Prometheus

- If repo is not set, the metrics will not show `repo=`.

```
total_running_vms 0
total_successful_runs_since_start 0
total_failed_runs_since_start 0
total_canceled_runs_since_start 1
plugin_status{name=RUNNER1,plugin=github,owner=veertuinc} idle
plugin_last_successful_run{name=RUNNER1,plugin=github,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_last_failed_run{name=RUNNER1,plugin=github,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_last_canceled_run{name=RUNNER1,plugin=github,owner=veertuinc,job_url=https://github.com/veertuinc/anklet/actions/runs/12325604197/job/34405145636} 0001-01-01T00:00:00Z
plugin_status_since{name=RUNNER1,plugin=github,owner=veertuinc} 2024-12-13T19:24:47-06:00
plugin_total_ran_vms{name=RUNNER1,plugin=github,owner=veertuinc} 0
plugin_total_successful_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 0
plugin_total_failed_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 0
plugin_total_canceled_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 1
plugin_status{name=RUNNER2,plugin=github,owner=veertuinc} idle
plugin_last_successful_run{name=RUNNER2,plugin=github,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_last_failed_run{name=RUNNER2,plugin=github,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_last_canceled_run{name=RUNNER2,plugin=github,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_status_since{name=RUNNER2,plugin=github,owner=veertuinc} 2024-12-13T19:24:47-06:00
plugin_total_ran_vms{name=RUNNER2,plugin=github,owner=veertuinc} 0
plugin_total_successful_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 0
plugin_total_failed_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 0
plugin_total_canceled_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 0
host_cpu_count 12
host_cpu_used_count 1
host_cpu_usage_percentage 12.371134
host_memory_total_bytes 38654705664
host_memory_used_bytes 19959037952
host_memory_available_bytes 18695667712
host_memory_usage_percentage 51.634174
host_disk_total_bytes 994662584320
host_disk_used_bytes 783564165120
host_disk_available_bytes 211098419200
host_disk_usage_percentage 78.776881
```

### Metrics Aggregator

In most cases each individual Anklet serving up their own metrics is good enough for your monitoring needs. However, there are situations where you may need to consume them from a single source instead. The Anklet Aggregator service is designed to do just that.

In order to enable the aggregator, you will want to run an Anklet with the `metrics > aggregator` flag set to `true`. **You want to run this separate from any other plugins.** This will start an Anklet Aggregator service that will collect metrics from all Anklet metrics stored in the Databse and make them available at `http://{aggregator_url}:{port}/metrics?format=json` or `http://{aggregator_url}:{port}/metrics?format=prometheus`. Here is an example config:

```yaml
---
work_dir: /Users/nathanpierce/anklet/
pid_file_dir: /tmp/
global_database_url: localhost
global_database_port: 6379
global_database_user: ""
global_database_password: ""
global_database_database: 0
metrics:
  aggregator: true
  port: 8081 # port to serve aggregator on
  sleep_interval: 10 # how often to fetch metrics from the Database (note: handlers push their metrics to the Database every 10 seconds)
```

You can see that this requires a database to be running. The aggregator will store the metrics in Redis so that it can serve them up without delay.

It's possible to use ENVs instead of the yml file. This is useful if you want to running anklet metrics aggregator in kubernetes. Here is a list of ENVs that you can use:

| ENV | Description |
| --- | --- |
| ANKLET_METRICS_AGGREGATOR | Whether to enable the aggregator (ex: true) |
| ANKLET_METRICS_PORT | Port to serve aggregator on (ex: 8081) |
| ANKLET_METRICS_SLEEP_INTERVAL | How many seconds between fetching metrics from each Anklet url defined |
| ANKLET_METRICS_DATABASE_ENABLED | Whether to enable the database (ex: true) |
| ANKLET_METRICS_DATABASE_URL | URL of the database (ex: localhost) |
| ANKLET_METRICS_DATABASE_PORT | Port of the database (ex: 6379) |
| ANKLET_METRICS_DATABASE_DATABASE | Database to use (ex: 0) |
| ANKLET_METRICS_DATABASE_USER | User to use (ex: "") |
| ANKLET_METRICS_DATABASE_PASSWORD | Password to use (ex: "") |

Finally, here are the example responses of each format:

#### JSON (/v1)

```json
{
  "GITHUB_RECEIVER": {
    "repo_name": "",
    "plugin_name": "github_receiver",
    "custom_name": "GITHUB_RECEIVER",
    "owner_name": "veertuinc",
    "last_update": "2025-01-07T18:17:03-06:00",
    "plugin_status": "running",
    "plugin_last_successful_run": "0001-01-01T00:00:00Z",
    "plugin_last_failed_run": "0001-01-01T00:00:00Z",
    "plugin_last_canceled_run": "0001-01-01T00:00:00Z",
    "plugin_status_since": "2025-01-07T18:07:36-06:00",
    "plugin_total_ran_vms": 0,
    "plugin_total_successful_runs_since_start": 0,
    "plugin_total_failed_runs_since_start": 0,
    "plugin_total_canceled_runs_since_start": 0,
    "plugin_last_successful_run_job_url": "",
    "plugin_last_failed_run_job_url": "",
    "plugin_last_canceled_run_job_url": "",
    "host_cpu_count": 12,
    "host_cpu_used_count": 3,
    "host_cpu_usage_percentage": 27.4378179763971,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 29228433408,
    "host_memory_available_bytes": 9426272256,
    "host_memory_usage_percentage": 75.6141662597656,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 623747604480,
    "host_disk_available_bytes": 370914979840,
    "host_disk_usage_percentage": 62.7094669401307
  },
  "RUNNER1": {
    "repo_name": "",
    "plugin_name": "github",
    "custom_name": "RUNNER1",
    "owner_name": "veertuinc",
    "last_update": "2025-01-07T18:16:59-06:00",
    "plugin_status": "idle",
    "plugin_last_successful_run": "2025-01-07T18:16:18-06:00",
    "plugin_last_failed_run": "0001-01-01T00:00:00Z",
    "plugin_last_canceled_run": "2025-01-07T18:09:26-06:00",
    "plugin_status_since": "2025-01-07T18:07:29-06:00",
    "plugin_total_ran_vms": 4,
    "plugin_total_successful_runs_since_start": 4,
    "plugin_total_failed_runs_since_start": 0,
    "plugin_total_canceled_runs_since_start": 2,
    "plugin_last_successful_run_job_url": "https://api.github.com/repos/veertuinc/anklet/actions/jobs/35284640962",
    "plugin_last_failed_run_job_url": "",
    "plugin_last_canceled_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/12661447359/job/35284636456",
    "host_cpu_count": 12,
    "host_cpu_used_count": 2,
    "host_cpu_usage_percentage": 24.2719743523022,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 27512193024,
    "host_memory_available_bytes": 11142512640,
    "host_memory_usage_percentage": 71.1742401123047,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 623187689472,
    "host_disk_available_bytes": 371474894848,
    "host_disk_usage_percentage": 62.6531749857708
  },
  "RUNNER2": {
    "repo_name": "",
    "plugin_name": "github",
    "custom_name": "RUNNER2",
    "owner_name": "veertuinc",
    "last_update": "2025-01-07T18:16:59-06:00",
    "plugin_status": "idle",
    "plugin_last_successful_run": "2025-01-07T18:16:15-06:00",
    "plugin_last_failed_run": "2025-01-07T18:10:40-06:00",
    "plugin_last_canceled_run": "0001-01-01T00:00:00Z",
    "plugin_status_since": "2025-01-07T18:07:29-06:00",
    "plugin_total_ran_vms": 4,
    "plugin_total_successful_runs_since_start": 3,
    "plugin_total_failed_runs_since_start": 1,
    "plugin_total_canceled_runs_since_start": 0,
    "plugin_last_successful_run_job_url": "https://api.github.com/repos/veertuinc/anklet/actions/jobs/35284640731",
    "plugin_last_failed_run_job_url": "https://api.github.com/repos/veertuinc/anklet/actions/jobs/35284636486",
    "plugin_last_canceled_run_job_url": "",
    "host_cpu_count": 12,
    "host_cpu_used_count": 2,
    "host_cpu_usage_percentage": 24.2719743523022,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 27512193024,
    "host_memory_available_bytes": 11142512640,
    "host_memory_usage_percentage": 71.1742401123047,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 623187689472,
    "host_disk_available_bytes": 371474894848,
    "host_disk_usage_percentage": 62.6531749857708
  }
}
```

#### JSON (/v2)

```json
[
  {
    "repo_name": "",
    "plugin_name": "github_receiver",
    "custom_name": "GITHUB_RECEIVER",
    "owner_name": "veertuinc",
    "last_update": "2025-02-19T17:27:10-06:00",
    "plugin_status": "running",
    "plugin_last_successful_run": "0001-01-01T00:00:00Z",
    "plugin_last_failed_run": "0001-01-01T00:00:00Z",
    "plugin_last_canceled_run": "0001-01-01T00:00:00Z",
    "plugin_status_since": "2025-02-19T17:24:20-06:00",
    "plugin_total_ran_vms": 0,
    "plugin_total_successful_runs_since_start": 0,
    "plugin_total_failed_runs_since_start": 0,
    "plugin_total_canceled_runs_since_start": 0,
    "plugin_last_successful_run_job_url": "",
    "plugin_last_failed_run_job_url": "",
    "plugin_last_canceled_run_job_url": "",
    "host_cpu_count": 12,
    "host_cpu_used_count": 2,
    "host_cpu_usage_percentage": 19.9605729629056,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 28348104704,
    "host_memory_available_bytes": 10306600960,
    "host_memory_usage_percentage": 73.3367496066623,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 752575967232,
    "host_disk_available_bytes": 242086617088,
    "host_disk_usage_percentage": 75.6614332433644
  },
  {
    "repo_name": "",
    "plugin_name": "github",
    "custom_name": "RUNNER1",
    "owner_name": "veertuinc",
    "last_update": "2025-02-19T17:27:11-06:00",
    "plugin_status": "running",
    "plugin_last_successful_run": "0001-01-01T00:00:00Z",
    "plugin_last_failed_run": "0001-01-01T00:00:00Z",
    "plugin_last_canceled_run": "2025-02-19T17:25:04-06:00",
    "plugin_status_since": "2025-02-19T17:26:58-06:00",
    "plugin_total_ran_vms": 2,
    "plugin_total_successful_runs_since_start": 0,
    "plugin_total_failed_runs_since_start": 0,
    "plugin_total_canceled_runs_since_start": 1,
    "plugin_last_successful_run_job_url": "",
    "plugin_last_failed_run_job_url": "",
    "plugin_last_canceled_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/13424339271/job/37504116762",
    "host_cpu_count": 12,
    "host_cpu_used_count": 2,
    "host_cpu_usage_percentage": 19.9605763305933,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 28236300288,
    "host_memory_available_bytes": 10418405376,
    "host_memory_usage_percentage": 73.0475107828776,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 752583897088,
    "host_disk_available_bytes": 242078687232,
    "host_disk_usage_percentage": 75.66223048417
  },
  {
    "repo_name": "",
    "plugin_name": "github",
    "custom_name": "RUNNER2",
    "owner_name": "veertuinc",
    "last_update": "2025-02-19T17:27:11-06:00",
    "plugin_status": "running",
    "plugin_last_successful_run": "2025-02-19T17:26:42-06:00",
    "plugin_last_failed_run": "2025-02-19T17:25:52-06:00",
    "plugin_last_canceled_run": "2025-02-19T17:25:04-06:00",
    "plugin_status_since": "2025-02-19T17:26:56-06:00",
    "plugin_total_ran_vms": 3,
    "plugin_total_successful_runs_since_start": 1,
    "plugin_total_failed_runs_since_start": 1,
    "plugin_total_canceled_runs_since_start": 1,
    "plugin_last_successful_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/13424340307/job/37504119547",
    "plugin_last_failed_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/13424339882/job/37504118023",
    "plugin_last_canceled_run_job_url": "https://github.com/veertuinc/anklet/actions/runs/13424339543/job/37504117422",
    "host_cpu_count": 12,
    "host_cpu_used_count": 2,
    "host_cpu_usage_percentage": 19.9605763305933,
    "host_memory_total_bytes": 38654705664,
    "host_memory_used_bytes": 28236300288,
    "host_memory_available_bytes": 10418405376,
    "host_memory_usage_percentage": 73.0475107828776,
    "host_disk_total_bytes": 994662584320,
    "host_disk_used_bytes": 752583897088,
    "host_disk_available_bytes": 242078687232,
    "host_disk_usage_percentage": 75.66223048417
  }
]
```

#### Prometheus

```
plugin_status{name=RUNNER1,owner=veertuinc} idle
plugin_last_successful_run{name=RUNNER1,owner=veertuinc,job_url=https://api.github.com/repos/veertuinc/anklet/actions/jobs/35269653289} 2025-01-07T11:59:21-06:00
plugin_last_failed_run{name=RUNNER1,owner=veertuinc,job_url=https://api.github.com/repos/veertuinc/anklet/actions/jobs/35269648602} 2025-01-07T11:56:47-06:00
plugin_last_canceled_run{name=RUNNER1,owner=veertuinc,job_url=https://github.com/veertuinc/anklet/actions/runs/12656636499/job/35269646979} 2025-01-07T11:55:53-06:00
plugin_status_since{name=RUNNER1,owner=veertuinc,status=idle} 2025-01-07T11:52:47-06:00
plugin_total_ran_vms{name=RUNNER1,plugin=github,owner=veertuinc} 5
plugin_total_successful_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 4
plugin_total_failed_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 1
plugin_total_canceled_runs_since_start{name=RUNNER1,plugin=github,owner=veertuinc} 1
host_cpu_count{name=RUNNER1,owner=veertuinc} 12
host_cpu_used_count{name=RUNNER1,owner=veertuinc} 1
host_cpu_usage_percentage{name=RUNNER1,owner=veertuinc} 12.390755
host_memory_total_bytes{name=RUNNER1,owner=veertuinc} 38654705664
host_memory_used_bytes{name=RUNNER1,owner=veertuinc} 24571248640
host_memory_available_bytes{name=RUNNER1,owner=veertuinc} 14083457024
host_memory_usage_percentage{name=RUNNER1,owner=veertuinc} 63.565996
host_disk_total_bytes{name=RUNNER1,owner=veertuinc} 994662584320
host_disk_used_bytes{name=RUNNER1,owner=veertuinc} 621074538496
host_disk_available_bytes{name=RUNNER1,owner=veertuinc} 373588045824
host_disk_usage_percentage{name=RUNNER1,owner=veertuinc} 62.440726
last_update{name=RUNNER1,owner=veertuinc} 2025-01-07T12:01:47-06:00
plugin_status{name=RUNNER2,owner=veertuinc} idle
plugin_last_successful_run{name=RUNNER2,owner=veertuinc,job_url=https://api.github.com/repos/veertuinc/anklet/actions/jobs/35269653543} 2025-01-07T11:59:29-06:00
plugin_last_failed_run{name=RUNNER2,owner=veertuinc,job_url=} 0001-01-01T00:00:00Z
plugin_last_canceled_run{name=RUNNER2,owner=veertuinc,job_url=https://github.com/veertuinc/anklet/actions/runs/12656636751/job/35269647515} 2025-01-07T11:55:56-06:00
plugin_status_since{name=RUNNER2,owner=veertuinc,status=idle} 2025-01-07T11:52:48-06:00
plugin_total_ran_vms{name=RUNNER2,plugin=github,owner=veertuinc} 5
plugin_total_successful_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 5
plugin_total_failed_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 0
plugin_total_canceled_runs_since_start{name=RUNNER2,plugin=github,owner=veertuinc} 1
host_cpu_count{name=RUNNER2,owner=veertuinc} 12
host_cpu_used_count{name=RUNNER2,owner=veertuinc} 1
host_cpu_usage_percentage{name=RUNNER2,owner=veertuinc} 12.390755
host_memory_total_bytes{name=RUNNER2,owner=veertuinc} 38654705664
host_memory_used_bytes{name=RUNNER2,owner=veertuinc} 24571248640
host_memory_available_bytes{name=RUNNER2,owner=veertuinc} 14083457024
host_memory_usage_percentage{name=RUNNER2,owner=veertuinc} 63.565996
host_disk_total_bytes{name=RUNNER2,owner=veertuinc} 994662584320
host_disk_used_bytes{name=RUNNER2,owner=veertuinc} 621074538496
host_disk_available_bytes{name=RUNNER2,owner=veertuinc} 373588045824
host_disk_usage_percentage{name=RUNNER2,owner=veertuinc} 62.440726
last_update{name=RUNNER2,owner=veertuinc} 2025-01-07T12:01:47-06:00
plugin_status{name=GITHUB_RECEIVER,owner=veertuinc} running
plugin_status_since{name=GITHUB_RECEIVER,owner=veertuinc,status=running} 2025-01-07T11:42:57-06:00
host_cpu_count{name=GITHUB_RECEIVER,owner=veertuinc} 12
host_cpu_used_count{name=GITHUB_RECEIVER,owner=veertuinc} 5
host_cpu_usage_percentage{name=GITHUB_RECEIVER,owner=veertuinc} 42.171734
host_memory_total_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 38654705664
host_memory_used_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 28486483968
host_memory_available_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 10168221696
host_memory_usage_percentage{name=GITHUB_RECEIVER,owner=veertuinc} 73.694738
host_disk_total_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 994662584320
host_disk_used_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 623034486784
host_disk_available_bytes{name=GITHUB_RECEIVER,owner=veertuinc} 371628097536
host_disk_usage_percentage{name=GITHUB_RECEIVER,owner=veertuinc} 62.637773
last_update{name=GITHUB_RECEIVER,owner=veertuinc} 2025-01-07T12:01:46-06:00
```

Important metrics are:

- `last_update` - updated every 10 seconds, you can use this to determine if the Anklet is sending metrics at all to the database by alarming if the timestamp is <= 11 seconds ago.

---

## Upgrading

### For AWS EC2 Mac (using the Cloud Connect [`ANKA_EXECUTE_SCRIPT`](https://docs.veertu.com/anka/aws-ec2-mac/#anka_execute_script-string))

Because Anklet is installed as a service, you need to stop the service, replace the binary, and start the service again. [The installation script is available](https://github.com/veertuinc/aws-ec2-mac-amis/blob/main/scripts/anklet-install.bash) for review so you can see where things are installed.

1. Download the latest release from the [releases page](https://github.com/veertuinc/anklet/releases)
1. Unzip
1. Transfer the `anklet` binary to the EC2 Mac instance
1. Log into the EC2 Mac instance with SSH.
1. Stop the running Anklet service: `sudo launchctl stop com.veertu.anklet`
1. Replace the existing `anklet` binary with the new one: `sudo chmod +x anklet_v0.12.2_darwin_arm64 && sudo mv anklet_v0.12.2_darwin_arm64 /usr/local/bin/anklet`
1. Start the Anklet service: `sudo launchctl start com.veertu.anklet`
1. Check the logs to ensure the service is running: `tail -100 /tmp/anklet-plist.out.log`

---

## Development

### Prepare your environment for development:

```bash
brew install go
go mod tidy
cd ${REPO_ROOT}
ln -s ~/.config/anklet/org-config.yml org-config.yml
ln -s ~/.config/anklet/repo-receiver-config.yml repo-receiver-config.yml
./run org-receiver-config.yml # run the receiver
./run org-config.yml # run the handler
```

- **NOTE:** You'll need to change the webhook URL so it points to the public IP of the server running the receiver (for me, that's my ISP's public IP + open port forwarding to my local machine).

#### Log Levels

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

### Plugins

Plugins are, currently, stored in the `plugins/` directory. They will be moved into external binaries at some point in the future.

Plugins should follow a pattern of: `Pick up job from DB` -> `Run Job` -> `Cleanup Job` -> `Optionally Return to DB if it needs to be retried`. Advanced plugins can also handle pausing the plugin, waiting for the other job running to finish, if there are not enough resources to run the job on the host yet, and let others hosts that can run it take it from the paused queue.

Plugins are loaded in the order they are listed in the config.yml file. We use the `workerGlobals.Plugins[plugin.Plugin][plugin.Name].Paused.Store(true)` and `workerGlobals.Plugins[plugin.Plugin][plugin.Name].FinishedInitialRun.Store(true)` to handle this. Your plugin logic MUST set this to `false` when it finishes running/does the cleanup phase.

Each plugin will also wait others of its type to finish "preparing" before they start. Once it's safe to allow other plugins to start, the plugin logic must perform `workerGlobals.Plugins[plugin.Plugin][plugin.Name].Preparing.Store(false)`.

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

Each plugin must have a `{name}.go` file with a `Run` function that takes in `context.Context`, etc . See [`handlers/github`](./plugins/handlers/github) plugin for an example.

The `Run` function should be designed to run multiple times in parallel between plugins. This means being aware of global context, locks, etc. It should not rely on any state from the previous runs and fully clean up at the end of each run.
    - Always `return` out of `Run` so the sleep interval in `main.go` can handle the next run properly with new context. Never loop inside of the plugin's `Run` function.
    - Should never panic but instead throw an ERROR and return. The `github` plugin has a go routine that loops and watches for completion of the job, context cancellation, or other signals, then performs cleanup before exiting in all situations. Be aware that context cancellation could prevent cleanup from happening, so use a context specifically for cleanup that is outside of the main context that got cancelled.
    - It's critical that you check for context cancellation after/before important logic that could orphan resources. The sooner you catch it, the better.

### Handling Metrics

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
./tests/cli-test.bash
```

Or run a specific test:

```bash
./tests/cli-test.bash start-stop
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

Plugin tests are end-to-end integration tests that validate receiver and handler plugins against real CI platforms. Tests are located in `tests/plugins/{plugin-name}/`.

Each test directory contains:

- **`manifest.yaml`** - Defines the test environment including required hosts, configurations, and startup scripts
- **`test.bash`** - The test script that runs assertions against workflow runs and logs
- **`*.yaml`** - Host-specific Anklet configuration files
- **`start-*.bash`** - Scripts to start Anklet components on each host

#### Manifest Structure

```yaml
description: "Test description"
tests:
    - name: "Test name"
      hosts:
        - name: "receiver"
          id: "ubuntu-22.04-linux"
          config: ubuntu-22.04-linux.yaml

        - name: "handler"
          id: "13-L-ARM-macos"
          config: 13-L-ARM-macos.yaml
```

#### Test Helper Functions

Tests have access to helper functions for common operations. See [`tests/plugins/AVAILABLE_TEST_FUNCTIONS.md`](./tests/plugins/AVAILABLE_TEST_FUNCTIONS.md) for the complete reference.

#### Example Test

[Example test for github plugin you can review](./tests/plugins/github/1-test-success).

### Unit Tests

Go unit tests can be run with:

```bash
make go.test
```

## Copyright

All rights reserved, Veertu Inc.