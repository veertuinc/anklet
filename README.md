# ANKLET

Inspired by our customer requirements, **Anklet** is a solution created to meet the specific needs of our users.

## At a glance

- Veertu's Anklet is a service that runs custom [plugins](./plugins) to communicate with your CI platform and the Anka CLI running on macOS hosts.
- It can run multiple plugins, in parallel, on the same host.
- Depending on the plugin, it can run on both linux containers/instances and macOS hosts. Plugins using the Anka CLI to spin up macOS VMs would need to run on a macOS host.

## Why Anklet?

Here are a few needs our customers expressed so you can understand the motivation for Anklet:

1. Each team and repository should not have knowledge of the Anka Build Cloud Controller URL, potential auth methods, Anka Node Groups, etc. These are all things that had to be set in the job yaml for our older github actions solution. This should be abstracted away for security and simplicity of use.
2. Developer CI workflow files cannot have multiple stages (start -> the actual job that runs in the VM -> a cleanup step) just to run a single Anka VM... that's just too much overhead to ask developers to manage. Instead, something should spin up the VM behind the scenes, register the runner, and then execute the job inside the VM directly.
3. They don't want the job to be responsible for cleaning up the VM + registered runner either. Something should watch the status of the job and clean up the VM when it's complete.

While these reasons are specific to Github Actions, they apply to many other CI platforms too. Let's get started!

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
    - You can run `anklet` with `LOG_LEVEL=DEBUG` to see more verbose output.
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
| ANKLET_GLOBAL_PRIVATE_KEY | Absolute path to private key for anklet (ex: /Users/myUser/.private-key.pem) |

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
- Using the ENVs: `ANKLET_GLOBAL_DATABASE_URL`, `ANKLET_GLOBAL_DATABASE_PORT`, `ANKLET_GLOBAL_DATABASE_USER`, `ANKLET_GLOBAL_DATABASE_PASSWORD`, `ANKLET_GLOBAL_DATABASE_DATABASE`.

### Plugin Setup and Usage Guides

You can control the location plugins are stored on the host by setting the `plugins_path` in the `config.yml` file. If not set, it will default to `~/.config/anklet/plugins/`. 

**NOTE: Plugin names MUST be unique across all hosts.**

#### Github Actions

- [**`Webhook Receiver Plugin`**](./plugins/receivers/github/README.md)
- [**`Workflow Run Job Handler Plugin`**](./plugins/handlers/github/README.md)

#### Docker / Containers

Docker images are available at [veertu/anklet](https://hub.docker.com/r/veertu/anklet). [You can find the example docker-compose file in the `docker` directory.](https://github.com/veertuinc/anklet/blob/main/docker/docker-compose.yml)

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
| plugin_status | Status of the plugin (idle, running, limit_paused, stopped) |
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
  sleep_interval: 10 # how often to fetch metrics from each Anklet defined
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
LOG_LEVEL=dev go run main.go -c org-receiver-config.yml # run the receiver
LOG_LEVEL=dev go run main.go -c org-config.yml # run the handler
```

- **NOTE:** You'll need to change the webhook URL so it points to the public IP of the server running the receiver (for me, that's my ISP's public IP + open port forwarding to my local machine).

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

Plugins are loaded in the order they are listed in the config.yml file. We use the `workerGlobals.Plugins[plugin.Plugin][plugin.Name].Paused.Store()` to handle this. Your plugin logic MUST set this to `false` when it handles cleanup.

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

Each plugin must have a `{name}.go` file with a `Run` function that takes in `context.Context`, `logger *slog.Logger`, etc . See `github` plugin for an example.

The `Run` function should be designed to run multiple times in parallel. It should not rely on any state from the previous runs.
    - Always `return` out of `Run` so the sleep interval and main.go can handle the next run properly with new context. Never loop inside of the plugin code.
    - Should never panic but instead throw an ERROR and return. The `github` plugin has a go routine that loops and watches for cancellation, which then performs cleanup before exiting in all situations except for crashes.
    - It's critical that you check for context cancellation after/before important logic that could orphan resources.

### Handling Metrics

Any of the plugins you run are done from within worker context. Each plugin also has a separate plugin context storing its Name, etc. The metrics for the anklet instance is stored in the worker context so they can be accessed by any of the plugins. Plugins should update the metrics for the plugin they are running in at the various phases.

For example, the `github` plugin will update the metrics for the plugin it is running in to be `running`, `pulling`, and `idle` when it is done or has yet to pick up a new job. To do this, it uses `metrics.UpdatePlugin` with the worker and plugin context. See `github` plugin for an example.

But metrics.UpdateService can also update things like `LastSuccess`, and `LastFailure`. See `metrics.UpdateService` for more information.

## FAQs

- Can I guarantee that the logs for Anklet will contain the `anklet (and all plugins) shut down` message?
  - No, there is no guarantee an error, not thrown from inside of a plugin, will do a graceful shutdown.

## Copyright

All rights reserved, Veertu Inc.