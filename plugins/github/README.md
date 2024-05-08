This plugin makes API requests to github's API to watch for workflow run jobs. It will then prepare a macOS VM, install the runner, and register it to the CI platform to run the job.

Since there are are limits for github API requests, we use [go-github-ratelimit/](https://github.com/gofri/go-github-ratelimit/) and our own primary rate limit logic to pause any active work when we hit the API rate limit. It's recommeded to increase the limits for API requests with github, but it's not necessary.

Workflow Run Jobs are processed in order of creation and then finally based on name. Sorting by name allows you the flexibility to run jobs in a specific order by naming them appropriately.

In the `config.yml`, you can define the `github` plugin as follows:

```
services:
  - name: RUNNER1
    plugin: github
    token: github_pat_XXX
    # Instead of PAT, you can create a github app for your org/repo and use its credentials instead.
    # private_key: /path/to/private/key
    # app_id: 12345678 # Settings > Developer > settings > GitHub App > About item
    # installation_id: 12345678 # Settings > Developer > settings > GitHub Apps > Advanced > Payload in Request tab
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

- Your PAT or Github App must have **Actions** and **Administration** Read & Write permissions.
- The `database` is required. You can find installation instructions in the anklet main [README.md](../../README.md#database-setup).


In your workflow yml's `runs-on`, you need to add several labels. Here is the list and an example:

1. `self-hosted` (required)
1. `anka` (required)
1. `anka-template:{UUID OF TEMPLATE HERE}` (required)
1. `anka-template-tag:{TAG NAME OF TEMPLATE HERE}` (optional; uses latest if not populated)
1. `run-id:${{ github.run_id }}` (do not change this) - label that is used to ensure that jobs in the same workspace don't compete for the same runner.
1. `unique-id:{UNIQUE ID OF JOB HERE}` - a label that is used to ensure multiple jobs in the same run don't compete for the same runner.

(from [t1-with-tag-1.yml](.github/workflows/t1-with-tag-1.yml))

```
name: 't1-with-tag-1'
on:
  workflow_dispatch:

jobs:
  testJob:
    runs-on: [ 
      "self-hosted", 
      "anka", 
      "anka-template:d792c6f6-198c-470f-9526-9c998efe7ab4", 
      "anka-template-tag:vanilla+port-forward-22+brew-git",
      "run-id:${{ github.run_id }}", 
      "unique-id:1"
    ]
    steps:
      - uses: actions/checkout@v3
      - run: |
          ls -laht
          sw_vers
          hostname
          echo "123"
```

Finally, the `github` plugin requires three different bash scripts available on the host, which it will copy into the VM and run. You can find them under https://github.com/veertuinc/anklet/tree/main/plugins/github. They can be customized to fit your needs. You'll place all three in `~/.config/anklet/plugins/github/`.