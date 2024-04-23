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
3. Run the daemon by executing `anklet` on the host that has the [Anka CLI installed](https://docs.veertu.com/anka/anka-virtualization-cli/getting-started/installing-the-anka-virtualization-package/).
    - `tail -fF /Users/myUser/Library/Logs/anklet.log` to see the logs. You can run `anklet` with `LOG_LEVEL=DEBUG` to see more verbose output.
3. To stop, you have two options:
    - `anklet -s stop` to stop the services semi-gracefully (interrupt the plugin at the next context cancellation definition, and still try to cleanup gracefully). This requires that the plugin has properly defined context cancellation checks.
    - `anklet -s drain` to stop services, but wait for all jobs to finish gracefully.

### Database Setup

At the moment we support `redis` 7.x for the database. It can be installed on macOS using homebrew:

```bash
brew install redis
sudo sysctl kern.ipc.somaxconn=511 # you can also add to /etc/sysctl.conf and reboot
brew services start redis
tail -fF /opt/homebrew/var/log/redis.log
```

While you can run it anywhere you want, its likely going to be less latency to host it on a host[s] that are in the same location at anklet. We recommend to choose one of the macs to run it on and point other hosts to it in their config. It's also possible to cluster redis, but we won't cover that in our guides.

### Plugin Setup and Usage Guides

- #### [**`Github Actions`**](./plugins/github/README.md)

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