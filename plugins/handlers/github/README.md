# GITHUB SERVICE PLUGIN

The Github Service Plugin is responsible for pulling a job from the database/queue, preparing a macOS VM, and registering it to the github repo as an action runner so it can execute the job inside.

Workflow Run Jobs are processed in order of creation. The Github Webhook Receiver Plugin will place the jobs in the database/queue in the order they're created. Be sure to run the [Receiver](../receivers/github) first!

Note: Our [Build Cloud Controller](https://docs.veertu.com/anka/anka-build-cloud/#anka-controller) (which is not used by Anklet at the moment) will run VMs under `sudo/root`, but we *do not require* that for anklet. It does not run as root, and will use the current user's space/environment to run VMs.

For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/blob/main/docs/database.md#database-setup).

In the `config.yml`, you can define the `github` plugin as follows:

```
plugins:
  - name: RUNNER1
    plugin: github
    token: github_pat_XXX
    # Instead of PAT, you can create a github app for your org/repo and use its credentials instead.
    # private_key: /path/to/private/key
    # app_id: 12345678 # Org > Settings > Developer settings > GitHub Apps > New GitHub App
    # installation_id: 12345678 # You need to install the app (Org > Settings > Developer settings > GitHub Apps > click Edit for the app > Install App > select your Repo > then check the URL bar for the installation ID)
    registration: repo
    repo: anklet # Optional; only needed if registering a specific runner for a repo, otherwise it will be an org level runner.
    owner: veertuinc
    registry_url: http://anka.registry:8089
    runner_group: macOS # requires Enterprise github
    # sleep_interval: 5 # Optional; defaults to 1 second.
    #database:
    #  enabled: true
    #  url: localhost
    #  port: 6379
    #  user: ""
    #  password: ""
    #  database: 0
```

- Your PAT or Github App must have **Actions** and **Administration** Read & Write permissions.
- You must define the database in the config.yml file either using the `database` section or the `global_database_*` variables. You can find installation instructions in the anklet main [README.md](../../README.md#database-setup).
- If you are attempting to register runners for an entire organization, do NOT set `repo` and make sure your Github App has `Self-hosted runners` > `Read and write` permissions.
- If your Organization level runner is registered and your public repo jobs are not picking it up even though the labels are a perfect match, make sure the Runner groups (likely `Default`) has `Allow public repositories`.

---

Next, in your workflow yml you need to add several labels to `runs-on`. Here is the list and an example:

1. `anka-template:{UUID OF TEMPLATE HERE}` (required)
1. `anka-template-tag:{TAG NAME OF TEMPLATE HERE}` (optional; uses latest if not populated)
<!-- 1. `run-id:${{ github.run_id }}` (do not change this) - label that is used to ensure that jobs in the same workspace don't compete for the same runner. -->
<!-- 1. `unique-id:{UNIQUE ID OF JOB HERE}` - a label that is used to ensure multiple jobs in the same run don't compete for the same runner. -->

**WARNING/NOTE:** If you are using the same template/tag/labels in `runs-on` for multiple runs/jobs, you will need to ensure that each job has a unique `unique-id` label or else they will compete for the same runner. To do this, set `"unique-id:${{ github.run_id }}"`. If you are starting multiple jobs in the same run, each job will need a unique `unique-id` label like `"unique-id:${{ github.run_id }}-1"` changing 1 to 2, 3, etc, for each job.

(from [t1-with-tag-1.yml](.github/workflows/t1-with-tag-1.yml))

```
name: 't1-with-tag-1'
on:
  workflow_dispatch:

jobs:
  testJob:
    runs-on: [ 
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

Finally, the `github` plugin requires three different bash scripts available on the host, which it will copy into the VM and run. You can find them under https://github.com/veertuinc/anklet/tree/main/plugins/handlers/github. They can be customized to fit your needs. Place them in under `${plugins_path}/handlers/github/`.

---

## API LIMITS

The following logic consumes [API limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28). Should you run out, all processing will pause until the limits are reset after the specific github duration and then resume where it left off.
  - Obtaining a registration token.
  - Should the runner be orphaned and not have been shut down cleanly so it unregistred itself, we will send a removal request.
  - Should the job request a template or tag that doesn't exist, we need to forcefully cancel the job in github or else other anklets will attempt processing indefinitely.