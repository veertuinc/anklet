# GITHUB HANDLER PLUGIN

**Be sure to set up the [Github Receiver Plugin](../../receivers/github/README.md) first!**

The Github Handler Plugin is responsible for pulling a job from the database/queue, preparing a macOS VM, and registering it to the github repo or organization as an action runner so it can execute the job inside.

The Github Handler Plugin will pull jobs from the database/queue in order of creation. The Github Webhook Receiver Plugin will place the jobs in the database/queue in the order they're created. 

Note: Our [Build Cloud Controller](https://docs.veertu.com/anka/anka-build-cloud/#anka-controller) (which is not used by Anklet at the moment) will run VMs under `sudo/root`, but we *do not require* that for anklet. It does not run as root, and will use the current user's space/environment to run VMs.

For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/tree/main?tab=readme-ov-file#database-setup).

In the `config.yml`, you can define the `github` plugin as follows:

**NOTE: Plugin `name` MUST be unique across all hosts and plugins.**

```yaml
---

. . .

plugins:
  - name: RUNNER1
    plugin: github
    # token: github_pat_XXX
    # Instead of PAT, you can create a github app for your org/repo and use its credentials instead.
    # private_key: /path/to/private/key
    # app_id: 12345678 # Org > Settings > Developer settings > GitHub Apps > New GitHub App
    # installation_id: 12345678 # You need to install the app (Org > Settings > Developer settings > GitHub Apps > click Edit for the app > Install App > select your Repo > then check the URL bar for the installation ID)
    registration: repo
    # repo: anklet # Optional; only needed if registering a specific runner for a repo, otherwise it will be an org level runner.
    owner: veertuinc
    registry_url: http://anka.registry:8089 # Optional; use `skip_pull` to skip pulling the template.
    # skip_pull: true # Optional; skips pulling the template from the Anka Build Cloud Registry
    runner_group: macOS # requires Enterprise github
    # sleep_interval: 5 # Optional; defaults to 1 second.
    # registration_timeout_seconds: 80 # Optional; defaults to 80 seconds.
    # template_disk_buffer: 10.0 # Optional; defaults to 10.0%. How much disk space to leave free on the host for templates.
    # job_retry_attempts: 10  # Override the default of 5
    #database:
    #  enabled: true
    #  url: localhost
    #  port: 6379
    #  user: ""
    #  password: ""
    #  database: 0
    #  max_retries: 5 # when a database operation fails due to a recoverable error, how many times to retry it before giving up
    #  retry_delay: 1000 # how long to wait between retries
    #  retry_backoff_factor: 2.0 # how much to multiply the retry delay by each time
    # template_disk_buffer: 10.0 # Optional; defaults to 10.0%. How much disk space to leave available and not allow templates to consume. Important when you have long running jobs that consume a lot of disk space while they run.
```

- Your PAT or Github App must have **Actions** and **Administration** Read & Write permissions.
- To avoid 404s from the Github API, you need to be sure the app is installed on the repos you want to run jobs on.
- You must define the database in the config.yml file either using the `database` section or the `global_database_*` variables. You can find installation instructions in the anklet main [README.md](../../README.md#database-setup).
- If you are attempting to register runners for an entire organization, do NOT set `repo` and make sure your Github App has `Self-hosted runners` > `Read and write` permissions.
- If your Organization level runner is registered and your public repo jobs are not picking it up even though the labels are a perfect match, make sure the Runner groups (likely `Default`) has `Allow public repositories`.
- There are times when github will not register the runner for some reason. `registration_timeout_seconds` is available to set the custom seconds to wait before considering the runner registration failed (and retry on a new VM).

---

Next, in your Github Actions workflow yml you need to/can add several labels to `runs-on`. Here is the list:

1. `${{ github.run_id }}-${{ strategy.job-index }}` (required; prevents others jobs with the same `anka-template` and `anka-template-tag` from competing for the current runner)
1. `anka-template:{UUID OF TEMPLATE HERE}` (required)
1. `anka-template-tag:{TAG NAME OF TEMPLATE HERE}` (optional; uses latest if not populated)

(from [t1-with-tag-1.yml](.github/workflows/t1-with-tag-1.yml))

```
name: 't1-with-tag-1'
on:
  workflow_dispatch:

jobs:
  testJob:
    runs-on: [ 
      "${{ github.run_id }}-${{ strategy.job-index }}",
      "anka-template:d792c6f6-198c-470f-9526-9c998efe7ab4", 
      "anka-template-tag:vanilla+port-forward-22+brew-git",
    ]
    steps:
      - uses: actions/checkout@v3
      - run: |
          ls -laht
          sw_vers
          hostname
          echo "123"
```

### Install Supporting Scripts

Finally, the `github` plugin requires three different bash scripts available on the host, which it will copy into the VM and run. You can find them under https://github.com/veertuinc/anklet/tree/main/plugins/handlers/github. They can be customized to fit your needs. Place them under `${plugins_path}/handlers/github/` (default `~/.config/anklet/plugins/handlers/github/`).

---

## Failure and Retry handling

Anklet does its best to handle failures gracefully. We attempt to retry the entire VM setup/registration process for failures that are transient and recoverable. This includes connection problems, Anka CLI failures that can be retried, and others.

If something cannot be safely retried, we have to send an API request to cancel the job in Github. Your users will see a cancelled job if there was an unrecoverable failure. Side note: We've asked Github to allow us to annotate the cancellation with a message so we can better understand why it was cancelled, but it's still pending: https://github.com/orgs/community/discussions/134326 (please up vote it!)

However, there are more complex failures that happen with the registration of the actions runner in the VM itself. At the moment of writing this, Github has a bug that assigns the same internal ID to runners that are registered around the same time. See https://github.com/actions/runner/issues/3621 for more info. This means we need to count the time between when we register and start the runner in the VM and when the webhook/job goes into `in_progress` status, indicating the job in Github is now running inside of the VM properly. IF it doesn't go into `in_progress` within X seconds (see the code), we consider it the github bug and will retry it on a brand new VM. You can set this timeout in the plugin config with `registration_timeout_seconds`.

---

## API LIMITS

The following logic consumes [API limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28). Should you run out, all processing will pause until the limits are reset after the specific github duration and then resume where it left off.

  1. Obtaining a registration token. (single call)
  2. Should the job request a template that doesn't exist, we need to forcefully cancel the job in github or else other anklets will attempt processing indefinitely.
  3. Should the runner be orphaned and not have been shut down cleanly, we will send a removal request to the API. (one to check if already removed, one to remove)
  4. If jobs were `in_progress` when anklet was exited, we will make a single API call to see if the job finished successfully or not. If the job is still running, the VM will stay running and the plugin will wait for it to finish. Otherwise, we'll proceed with the cleanup.
  5. If a job failed for some reason and is being retried, on the next attempts to run the job, we will make a single API call to see if the job finished successfully or not in github. This is necessary since github will sometimes not send updates to the webhook. There is a limit of 5 attempts before we cancel the job forcefully.

This means worst case scenario it could make a total of 3 api calls total. Otherwise only one should happen on job success.

---

## FAQS

1. My Jobs are taking a long time to be picked up.

This can happen for several reasons. It's important to understand that the Github Actions plugin will go one-by-one through the queue starting with the eldest item to find a job it can run. This is necessary so that the plugins can clean up the queue of older cancelled jobs that are still queued in the DB.

Check that:

  1. You don't have too many queued jobs piled up. If your target template needs 12 CPU cores, but almost all of your hosts only have 8 cores, the few hosts with 12 may take a while to get the job off the queue and out of the way. Remember, the plugin on completion of a job will reset the queue target index so it starts from the beginning of the queue. It will need to crawl through all of the jobs it can't run each time.
  2. The jobs you're running are just long running jobs. You may need more hosts to handle the demand.
  3. You're specifying `${{ github.run_id }}-${{ strategy.job-index }}` in your `runs-on` labels. Otherwise, jobs could timeout or fail and cause delays in cleanup, etc.

2. Debug logging can be enabled with `LOG_LEVEL=dev` in the environment. All output of debug logging will be in JSON.

3. Available `plugin_status` values are: `running`, `in_progress`, `limit_paused`, `idle`, `stopped`.
  - `running`: The plugin has started and is available to run a job.
  - `in_progress`: The plugin has picked up a job to run.
  - `limit_paused`: The plugin is paused because of Github API rate limits. (will continue once the rate limits are reset after the specific github duration)
  - `idle`: The plugin is idle.
  - `stopped`: The plugin is stopped.

## Development

This plugin handles running jobs queued in the DB. It checks queued items one by one (starting at 0 index) to find one that it can run. It is also responsible for finding completed jobs in the DB and cleaning the jobs up, even if it can't run the job according to requirements.

When first starting up, the plugin will do an initial check to see if it has any job that was running last time it stopped. It picks up where it left off if so. Other plugins will be paused while this happens until it's their turn (`workerGlobals.IsAPluginPreparingState()`). They do this one by one in the order they're listed in the config.

### Functions

#### `cleanup`

This function runs at the end of the plugin's run, ensuring that everything is cleaned up.

- It uses its own context to avoid context cancellation preventing it from running. It must run or things will be orphaned!
- It pops items off the plugin's queue one by one in the newest first order and handles them. For example, a job that runs fully will have index 0,1 with 0 being the newest object inserted in the plugin queue and representing the anka.VM. The function sees this and processes the deletion of the VM, then moves on to the next object until there is nothing left to cleanup.

#### `checkForCompletedJobs`

This is running constantly for the entire life of the plugin. It constantly checks the main completed queue for a job that match the currently active job ID. If it finds one, it sets the existing job's status, etc.

Outside of the mainCompletedQueue getting an item, you can interrupt it several other ways:

1. `workerGlobals.ReturnAllToMainQueue` - This is used by the main worker process in main.go to tell the plugin to return all of its jobs to the main queue so other hosts can handle them.
2. `<-pluginCtx.Done()` - context cancellation case.
3. `<-pluginGlobals.JobChannel` - Other parts of the plugin logic can inject the `internalGithub.QueueJob` object into this and then handle specific logic for the job. For example, we can handle if `.Action` being `finish` (immediate cleanup) or `cancel` (send a cancel request to github, then cleanup): `pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}`. Important:For `cancel`, you need to pass the entire queued job object.
4. `<-pluginGlobals.RetryChannel` - This will send the job back to the mainQueue so other hosts can get a chance to run it.
5. `<-pluginGlobals.PausedCancellationJobChannel` - This cleans up the job immediately on this host, since another host has picked it up. We could technically just use `finish` here as the Action, but it will allow us more control over logic that only happens when the paused job is removed from this host.

#### `watchJobStatus`

This function loops and waits for the job to be in a completed status in the specific plugin's queue.

#### `sendCancelWorkflowRun`

This function sends a cancel request to the github API, preventing the runner from being orphaned in github.

#### `Run`

The primary function that runs the plugin.

- On start we run a full `checkForCompletedJobs` and `cleanup` in order to continue where we left off if the plugin was stopped mid-run.
- In versions >= `0.14.0`, we support handling VMs with varying resource requirements. To do this, we need to check the VM Template's needs and what we have currently available. This requires that plugins don't start VMs at the same time or else we won't have usage information to compare to. `workerGlobals.SetAPluginIsPreparing(pluginConfig.Name)` is used to enable us to only allow one plugin at a time to start VMs for safety. It unlocks after preparing the VM.
- Each `return` from this function must include some sort of instruction to interrupt the `checkForCompletedJobs` otherwise it will run indefinitely. This is done by sending a message to the `pluginGlobals.JobChannel`: `pluginGlobals.JobChannel <- internalGithub.QueueJob{Action: "finish"}`

#### Preparing your local machine for development

```
# clone into ~/anklet, otherwise change this
mkdir -p ~/.config/anklet
ln -s ~/anklet/plugins /Users/veertu/.config/anklet/plugins
```