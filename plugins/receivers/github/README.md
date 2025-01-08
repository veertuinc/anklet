# GITHUB RECEIVER PLUGIN

The Github Receiver Plugin is used to receive webhook events from github and store them in the database for the [Github Handler Plugin](../../handlers/github/README.md) to pick up and process.

### What you need:

1. An active database the receiver can access. For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/tree/main?tab=readme-ov-file#database-setup). It needs to be the same database as the [Github Handler Plugin](../../handlers/github/README.md).
1. Some sort of Auth method like a PAT or a Github App for the repo you want to receive webhooks for. They need `Administration`, `Webhooks`, and `Actions` set to `Read and write`.
1. A way to receive webhooks. This can be a public URL or IP that points to the server running the Anklet Github Receiver. Github will send the webhook to this endpoint over the internet.

In the `config.yml`, you can define the `github_receiver` plugin as follows:

**NOTE: Plugin `name` MUST be unique across all hosts and plugins in your Anklet cluster.**

```
---

. . .

global_receiver_secret: 12345 # this can be set using the ANKLET_GLOBAL_RECEIVER_SECRET env var too
plugins:
  - name: GITHUB_WEBHOOK_RECEIVER_1
    plugin: github_receiver
    hook_id: 4897477123
    port: 54321
    # secret: 12345
    # private_key: /Users/{YOUR USER HERE}/private-key.pem
    app_id: 949431
    installation_id: 52970581
    # repo: anklet # Optional; if you want to receive webhooks for a specific repo and not at the org level
    owner: veertuinc
    # skip_redeliver: true # Optional; if you want to skip redelivering undelivered webhooks on startup
    # redeliver_hours: 24 # Optional; default is 24 hours
    #database:
    #  url: localhost
    #  port: 6379
    #  user: ""
    #  password: ""
    #  database: 0
```

Some things to note:

- If you leave off `repo`, the receiver will be an organization level receiver.
- The receiver must come FIRST in the `plugins:` list. Do not place it after other plugins.
- **IMPORTANT**: On first start, it will scan for failed webhook deliveries for the past 24 hours and send a re-delivery request for each one. This is to ensure that all webhooks are delivered and processed and nothing in your plugins are orphaned or database. Avoid excessive restarts or else you'll eat up your API limits quickly. You can use `skip_redeliver: true` to disable this behavior.

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

## API Limits

The following logic consumes [API limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28). Should you run out, all processing will pause until the limits are reset after the specific github duration and then resume where it left off.
  - Requesting all hook deliveries for the past 24 hours.
  - To get verbose information for each hook delivery that's `in_progress` still.
  - Then again to post the redelivery request if all other conditions are met indicating it was orphaned.

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

<!-- ```
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
} -->
```

---

## Healthcheck

A Healthcheck endpoint is available at `http://{url/ip}:{port}/healthcheck`. It will return a 200 status code and `ok` if the plugin is running.
