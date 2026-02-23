# BUILDKITE RECEIVER PLUGIN

This receiver accepts Buildkite webhook events and writes queue items for the Buildkite handler plugin.

## Supported webhook events

- `job.scheduled` -> queued
- `job.started` -> in_progress
- `job.finished` -> completed

## Config example

```yaml
plugins:
  - name: BUILDKITE_WEBHOOK_RECEIVER_1
    plugin: buildkite_receiver
    port: 54321
    secret: your-webhook-secret
    owner: your-buildkite-org
    queue_name: anklet-tester
```

Notes:

- `name` must be unique across hosts/plugins.
- Keep receiver before handlers in `plugins:` so startup queue cleanup/listening order is deterministic.

## Endpoint

- `/jobs/v1/receiver`

## Buildkite webhook setup

In Buildkite Notification Services:

1. Create a Webhook target pointing to `http://<host-or-url>:<port>/jobs/v1/receiver`.
2. Subscribe at least to:
   - `job.scheduled`
   - `job.started`
   - `job.finished`
3. Configure webhook token/signature secret to match `secret` in Anklet config.

See Buildkite docs:

- https://buildkite.com/docs/apis/webhooks/pipelines
- https://buildkite.com/docs/apis/webhooks/pipelines/job-events

## Healthcheck

`http://<host-or-url>:<port>/healthcheck` returns `200 ok` while running.