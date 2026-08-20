# GITHUB RECEIVER PLUGIN

The Github Receiver Plugin receives webhook events from GitHub and stores them in the database for the [Github Handler Plugin](../../handlers/github/README.md) to process.

### What you need (every scope)

1. The same Redis database as your handler(s). See [Database Setup](https://github.com/veertuinc/anklet/tree/edge?tab=readme-ov-file#database-setup).
1. A public URL or IP that reaches this host so GitHub can POST webhooks (path `/jobs/v1/receiver`).
1. A shared webhook secret (`secret` on the plugin or `global_receiver_secret` / `ANKLET_GLOBAL_RECEIVER_SECRET`).

**NOTE: Plugin `name` MUST be unique across all hosts and plugins in your Anklet cluster.**

Pair each receiver with a handler at the **same scope** (same queue namespace). Put the receiver **first** in `plugins:` so it clears `in_progress` and listens before handlers start. Do not mix `enterprise` with `owner`/`repo` on one plugin.

## Choosing a GitHub scope

| Scope | Config | Webhook location | Use when |
| ----- | ------ | ---------------- | -------- |
| Repository | `owner` + `repo` | Repo → Settings → Webhooks | One repo only |
| Organization | `owner` only | Org → Settings → Webhooks | One org, all (or many) of its repos |
| Enterprise | `enterprise` only | Enterprise → Settings → Hooks | Enterprise Cloud; one hook for every org under the enterprise |

Pick the same scope for the matching [handler](../../handlers/github/README.md#choosing-a-github-scope). Most teams start with organization.

---

## Repository scope

### When to use

Only one repository should enqueue jobs. Useful for a single product repo or for isolating a sensitive repo from the rest of the org.

### Pros

- Smallest webhook and queue surface
- Repo-level webhook is easy to find and disable
- Failed-delivery redelivery via GitHub API on startup (org/repo)

### Cons

- One webhook + receiver per repo as you scale
- Broader org traffic never reaches this queue (by design)

### Config

```yaml
global_receiver_secret: 12345
plugins:
  - name: GITHUB_WEBHOOK_RECEIVER_REPO
    plugin: github_receiver
    hook_id: 4897477123
    port: 54321
    owner: veertuinc
    repo: anklet
    token: github_pat_XXX
    # or GitHub App:
    # private_key: /Users/{YOUR USER HERE}/private-key.pem
    # app_id: 949431
    # installation_id: 52970581
    # skip_redeliver: true
    # redeliver_hours: 24
```

### Auth

PAT or GitHub App with Repository **Administration**, **Webhooks**, and **Actions** Read and write (needed for hook-delivery redelivery). Check email for permission-request verification.

### Setup

1. Add a [repository webhook](#organization-or-repository-webhook) (Workflow jobs) pointing at `http(s)://{host}:{port}/jobs/v1/receiver` with the same secret.
2. Set `hook_id` from the webhook’s URL/settings after creation.
3. Run Anklet; confirm Redis keys under `anklet/jobs/github/queued/{owner}` (or your `queue_name`).

---

## Organization scope

### When to use

One GitHub organization should feed a shared Anklet queue for its repositories. Default choice for a single-org fleet.

### Pros

- One webhook covers the org
- API redelivery of failed deliveries on startup (disable with `skip_redeliver: true`)
- Matches the common org-level handler setup

### Cons

- Broader than repo scope; every matching `workflow_job` in the org can hit the queue (filtered by Anklet labels)
- Multiple orgs need multiple receivers (or enterprise / shared-queue patterns — [running multiple instances](../../../docs/running-multiple-instances.md))

### Config

```yaml
global_receiver_secret: 12345
plugins:
  - name: GITHUB_WEBHOOK_RECEIVER_1
    plugin: github_receiver
    hook_id: 4897477123
    port: 54321
    owner: veertuinc
    token: github_pat_XXX
    # private_key: /Users/{YOUR USER HERE}/private-key.pem
    # app_id: 949431
    # installation_id: 52970581
    # queue_name: shared_queue
    # skip_redeliver: true
    # redeliver_hours: 24
```

### Auth

PAT or GitHub App. Organization permissions: **Administration**, **Webhooks**, and **Self-hosted runners** Read and write. Verify permission requests in email.

### Setup

1. Add an [organization webhook](#organization-or-repository-webhook) (Workflow jobs).
2. Set `hook_id`, leave `repo` unset, leave `enterprise` unset.
3. On startup Anklet lists failed deliveries for the last `redeliver_hours` (default 24) and redelivers as needed. Avoid restart loops; they burn API quota. Use `skip_redeliver: true` to turn that off.

---

## Enterprise scope (GitHub Enterprise Cloud)

### When to use

All orgs under one Enterprise Cloud account should share one webhook and one queue. Pair with an [enterprise handler](../../handlers/github/README.md#enterprise-scope-github-enterprise-cloud). Do not set `owner` or `repo`.

### Pros

- One Hooks webhook for every org under the enterprise
- Receiver needs only the webhook secret (no PAT/App for live ingest)
- Fewer Anklet receiver instances than one-per-org

### Cons

- No public REST API for enterprise hook deliveries — Anklet always skips API redelivery; use the GitHub enterprise Hooks UI to redeliver failures
- Handler still needs classic PAT + GitHub App (receiver does not)
- Needs Enterprise Cloud admin access to create Hooks
- Easy to mis-pair with an org-scoped handler (queues will not match)

### Config

```yaml
global_receiver_secret: 12345
plugins:
  - name: GITHUB_WEBHOOK_RECEIVER_ENTERPRISE
    plugin: github_receiver
    port: 54321
    enterprise: veertu-inc
    # skip_redeliver always enforced (no public hook-delivery API)
    # App/PAT optional on the receiver; required on the enterprise handler
```

### Auth

Live ingest: webhook secret only. No PAT/App on the receiver. The matching **handler** still needs classic PAT (`manage_runners:enterprise`) plus a GitHub App with Repository **Actions** + **Administration** Read and write — see the [handler README](../../handlers/github/README.md#enterprise-scope-github-enterprise-cloud).

### Setup

1. Add an [enterprise Hooks webhook](#enterprise-global-webhook) (Workflow jobs).
2. Set `enterprise` to the enterprise slug; omit `owner`/`repo`.
3. Trigger a workflow that uses an `anka-template:...` label under any org in the enterprise; confirm Redis has a job under `anklet/jobs/github/queued/{enterprise}`.

---

Once configured, you can run Anklet and, if everything is configured properly, you should see logs like this:

```
{"time":"2025-01-06T10:38:23.198354-06:00","level":"INFO","msg":"starting plugin","ankletVersion":"0.11.2","pluginName":"GITHUB_WEBHOOK_RECEIVER"}
{"time":"2025-01-06T10:38:23.199399-06:00","level":"INFO","msg":"listing hook deliveries for the last 24 hours to see if any need redelivery (may take a while)...","ankletVersion":"0.11.2","pluginName":"GITHUB_WEBHOOK_RECEIVER","plugin":"github_receiver"}
{"time":"2025-01-06T10:38:27.186532-06:00","level":"INFO","msg":"started plugin","ankletVersion":"0.11.2","pluginName":"GITHUB_WEBHOOK_RECEIVER","plugin":"github_receiver"}
```

It should now be ready to receive webhooks. You can now set up a webhook to send events to this receiver.

## API Endpoints

- `/jobs/v1/receiver` - This is the endpoint that Github will send the webhook to. This is where the receiver will receive the webhook and store it in the database.

## Webhook Trigger Setup

### Organization or repository webhook

1. Find your repo (or organization) in github.com
1. Click on `Settings`
1. Click on `Webhooks`
1. Click on `Add webhook`
1. Set the `Payload URL` to the Public IP or URL that points to the server running the Anklet Github Receiver + `/jobs/v1/receiver`. So for example: `http://{PUBLIC IP OR URL}:54321/jobs/v1/receiver`
1. Set `Content Type` to `application/json`
1. Set the `Secret` to the `secret` from the `config.yml`
1. `SSL verfifcation` is up to you.
1. Choose `Workflow jobs` as the event to trigger/receive
1. Make sure `Active` is enabled
1. Click on `Add webhook`

### Enterprise (global) webhook

1. Open your enterprise on github.com (for example `https://github.com/enterprises/my-enterprise-name`)
1. Click `Settings`, then `Hooks`
1. Click `Add webhook`
1. Set the `Payload URL` to `https://{PUBLIC IP OR URL}:{port}/jobs/v1/receiver`
1. Set `Content Type` to `application/json`
1. Set the `Secret` to match `global_receiver_secret` or the plugin `secret`
1. Choose **Workflow jobs** as the event
1. Make sure `Active` is enabled
1. Click `Add webhook`

To verify: trigger a workflow under any org in the enterprise that uses an `anka-template:...` runner label, then confirm Redis has a job under `anklet/jobs/github/queued/{enterprise}` (for example `anklet/jobs/github/queued/veertu-inc`).

## API Limits

Incoming webhooks at `/jobs/v1/receiver` do not call the GitHub REST API.

[REST quota](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28) is used only on process start, when Anklet redelivers failed hook deliveries (once per start). If you hit the limit, Anklet pauses until GitHub resets it, then continues. Avoid restart loops; they run this walk again. Here are the calls that are made:

  - List deliveries for the last `redeliver_hours` (default 24), 100 per page. All deliveries for that hook, not only Anklet jobs.
  - Get the full payload for each failed delivery that is not `in_progress` and has no later successful redelivery of the same GUID.
  - For each of those failed `queued` deliveries, Get later `completed` deliveries in the same repo until the workflow job IDs match (or the window ends).
  - POST redelivery if the job still looks orphaned.

Enterprise scope never does this walk (no hook-delivery API). Set `skip_redeliver: true` to skip it on org/repo scope. Catch up from the GitHub webhook UI instead.

A PAT or GitHub App shared with [handlers](../../handlers/github/README.md#api-limits) is one quota bucket. Handler calls count against the same limit.

---

## Metrics

#### Key Names and Descriptions

| Key | Description | 
| ------ | ----------- |
| plugin_status | Status of the service (idle, running, limit_paused, stopped) |
| plugin_status_since | Time the plugin status was last updated |
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

```
❯ curl -s http://127.0.0.1:8080/metrics/v1\?format\=prometheus
plugin_status{name=GITHUB_RECEIVER,plugin=github_receiver,owner=veertuinc} running
plugin_status_since{name=GITHUB_RECEIVER,plugin=github_receiver,owner=veertuinc} 2025-01-07T11:42:57-06:00
host_cpu_count 12
host_cpu_used_count 5
host_cpu_usage_percentage 42.171734
host_memory_total_bytes 38654705664
host_memory_used_bytes 28486483968
host_memory_available_bytes 10168221696
host_memory_usage_percentage 73.694738
host_disk_total_bytes 994662584320
host_disk_used_bytes 623034486784
host_disk_available_bytes 371628097536
host_disk_usage_percentage 62.637773
```

```
❯ curl -s http://127.0.0.1:8080/metrics/v1\?format\=json | jq
{
  "host_cpu_count": 12,
  "host_cpu_used_count": 1,
  "host_cpu_usage_percentage": 12.126155512441432,
  "host_memory_total_bytes": 38654705664,
  "host_memory_used_bytes": 19213762560,
  "host_memory_available_bytes": 19440943104,
  "host_memory_usage_percentage": 49.70614115397135,
  "host_disk_total_bytes": 994662584320,
  "host_disk_used_bytes": 537423970304,
  "host_disk_available_bytes": 457238614016,
  "host_disk_usage_percentage": 54.03078177223378,
  "plugins": [
    {
      "name": "github_receiver",
      "plugin_name": "github_receiver",
      "repo_name": "anklet",
      "owner_name": "veertuinc",
      "status": "running",
      "status_since": "2024-08-20T14:58:35.730418-05:00"
    }
  ]
}
```

---

## Healthcheck

A Healthcheck endpoint is available at `http://{url/ip}:{port}/healthcheck`. It will return a 200 status code and `ok` if the plugin is running.

## FAQS

1. Available `plugin_status` values are: `running`, `in_progress`, `limit_paused`, `idle`, `stopped`.
  - `running`: The plugin has started and is available to run a job.
  - `in_progress`: The plugin has picked up a job to run.
  - `limit_paused`: The plugin is paused because of Github API rate limits. (will continue once the rate limits are reset after the specific github duration)
  - `idle`: The plugin is idle.
  - `stopped`: The plugin is stopped.