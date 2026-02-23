# BUILDKITE HANDLER PLUGIN

Set up the [Buildkite Receiver Plugin](../../receivers/buildkite/README.md) first.

This plugin pulls Buildkite jobs from Redis, prepares an Anka VM, installs a Buildkite agent in that VM, and starts the agent with `--acquire-job` so only the intended job runs in that VM.

## Required isolation model

- Use a dedicated Buildkite queue (for example `anklet-tester`).
- Require a unique selector tag (for example `anklet=true`) in job agent rules.
- Do not allow general/static agents to register with that queue/tag combination.

This prevents jobs from being picked up by non-Anklet agents.

## Config example

```yaml
plugins:
  - name: BUILDKITE_HANDLER_1
    plugin: buildkite
    token: bk_agent_token_xxx
    owner: your-buildkite-org
    # repo: your-pipeline-slug # optional, docs/metadata only
    queue_name: anklet-tester
    registry_url: http://anka.registry:8089
    # skip_pull: true
```

## Template/tag selector rules

The handler resolves template selectors in this order:

1. From Buildkite agent query rules/tags:
   - `anka-template:<uuid>`
   - `anka-template-tag:<tag>`
2. Fallback env vars:
   - `ANKA_TEMPLATE_UUID`
   - `ANKA_TEMPLATE_TAG`

## Supporting scripts

The plugin expects these scripts in `${plugins_path}/handlers/buildkite/`:

- `install-runner.bash`
- `register-runner.bash`
- `start-runner.bash`

They are copied into each VM and executed there.