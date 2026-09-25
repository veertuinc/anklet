# Manual webhook delivery to the GitHub receiver

Use this when GitHub cannot reach your receiver (firewall, tunnel down, wrong URL) but you still have the webhook payload and want Anklet to process the job.

The receiver accepts **Workflow jobs** webhooks (`workflow_job` events). That is the JSON GitHub sends when a job in a workflow run is queued, starts, or finishes. It is not a `workflow_run` event.

## When to use manual delivery

Try these first when they fit your setup:

| Method | Scope | Notes |
| ------ | ----- | ----- |
| **Redeliver** in GitHub webhook UI | All | Repo/org: Settings → Webhooks → Recent Deliveries → Redeliver. Enterprise: Settings → Hooks → Recent Deliveries. |
| **Anklet startup redelivery** | Org/repo | On start, Anklet lists failed deliveries and asks GitHub to redeliver. See [receiver README](../plugins/receivers/github/README.md#api-limits). Set `skip_redeliver: true` to disable. |
| **GitHub API redeliver** | Org/repo | `POST /repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}/attempts` (or the org equivalent). |

Use manual POST when redelivery is blocked, you need to replay one delivery outside GitHub, or you are testing with a saved payload.

## Prerequisites

1. The GitHub receiver plugin is running and listening on its configured `port`.
2. You know the webhook **secret** (`secret` on the plugin, or `global_receiver_secret` / `ANKLET_GLOBAL_RECEIVER_SECRET`).
3. The payload is a **`workflow_job`** event with `action` set to `queued`, `in_progress`, or `completed`.
4. The job labels include **`anka-template`** (for example `anka-template:macos-14`). Without that label, the receiver logs the event but does not enqueue it.

## Get the raw payload

### From GitHub (webhook UI)

1. Open the webhook (repo/org Settings → Webhooks, or enterprise Settings → Hooks).
2. Open **Recent Deliveries** and select the failed or missing delivery.
3. Copy the **Request** body (JSON). Save it to a file, for example `payload.json`.

### From the GitHub API (org/repo)

Fetch the delivery and read `request.payload` (raw JSON):

```bash
# Repository webhook
gh api "repos/{owner}/{repo}/hooks/{hook_id}/deliveries/{delivery_id}" \
  --jq '.request.payload' > payload.json

# Organization webhook
gh api "orgs/{owner}/hooks/{hook_id}/deliveries/{delivery_id}" \
  --jq '.request.payload' > payload.json
```

Enterprise global hooks have no public delivery API. Use the Hooks UI or a saved payload.

## Sign and POST the payload

The receiver validates the body with HMAC-SHA256, same as GitHub. You must send:

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/json` |
| `X-GitHub-Event` | `workflow_job` |
| `X-Hub-Signature-256` | `sha256=<hex digest of HMAC-SHA256(body, secret)>` |
| `X-GitHub-Delivery` | Any unique string (optional but useful in logs) |

### Compute the signature

```bash
SECRET='your-webhook-secret'
SIG="sha256=$(openssl dgst -sha256 -hmac "$SECRET" payload.json | awk '{print $2}')"
echo "$SIG"
```

### Send the request

Replace host, port, and paths to match your receiver config.

```bash
RECEIVER_URL='http://127.0.0.1:54321/jobs/v1/receiver'
DELIVERY_ID="manual-$(uuidgen)"

curl -sS -X POST "$RECEIVER_URL" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: workflow_job" \
  -H "X-GitHub-Delivery: ${DELIVERY_ID}" \
  -H "X-Hub-Signature-256: ${SIG}" \
  --data-binary @payload.json
```

For a remote receiver, use the public URL or IP GitHub would use (same path: `/jobs/v1/receiver`).

### Minimal payload shape (for tests)

Real payloads from GitHub are larger. For a local test, structure must match what the receiver parses: top-level `action`, `workflow_job`, and `repository` (owner may be an object with `login`).

```json
{
  "action": "queued",
  "workflow_job": {
    "id": 123456789,
    "run_id": 987654321,
    "name": "build",
    "status": "queued",
    "labels": ["anka-template:your-template"],
    "html_url": "https://github.com/org/repo/actions/runs/987654321/job/123456789",
    "workflow_name": "CI"
  },
  "repository": {
    "name": "repo",
    "owner": { "login": "org" },
    "private": true
  }
}
```

Use real `id` and `run_id` values from GitHub if you expect the handler to call the Actions API for that job.

## Verify it worked

1. **Anklet logs** (receiver plugin): look for `received workflow job to consider` and, for a new queued job with `anka-template`, `job pushed to queued queue`.
2. **Redis**: a new entry under `anklet/jobs/github/queued/{queue_owner}` where `queue_owner` is `owner`, `enterprise`, or your `queue_name` depending on receiver scope.
3. **HTTP status**: a usable `workflow_job` returns `200`. A malformed, unsigned, or unparseable payload returns `400` and is not queued. Missing optional fields (name, status, run_id, repository, owner) still return `200`.

The receiver skips duplicate `queued` events if the job is already in the queued or handler queues.

## Troubleshooting

| Symptom | Likely cause |
| ------- | ------------- |
| HTTP `400` / log: `rejecting malformed webhook` | Wrong secret or signature, invalid JSON, wrong `X-GitHub-Event`, or a `workflow_job` missing `action` or `workflow_job.id`. |
| Event received, no queue push | Labels missing `anka-template`, or `action` is not `queued` / `in_progress` / `completed`, or job already in queue. |
| Connection refused | Receiver not running, wrong port, or firewall blocking the host. |

## Related docs

- [GitHub receiver plugin](../plugins/receivers/github/README.md)
- [Validating webhook deliveries (GitHub)](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)
