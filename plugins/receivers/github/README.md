# GITHUB RECEIVER PLUGIN

The Github Receiver Plugin is used to receive webhook events from github and store them in the database for the Github Service Plugin to pick up and process.

### What you need:

1. An active database the receiver can access. For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/blob/main/docs/database.md#database-setup).
1. Some sort of Auth method like a PAT or a Github App for the repo you want to receive webhooks about. They need `Administration`, `Webhooks`, and `Actions` set to `Read and write`.
1. A way to receive webhooks. This can be a public URL or IP that points to the server running the Anklet Github Receiver.

In the `config.yml`, you can define the `github_receiver` plugin as follows:

```
global_receiver_secret: 12345 # this can be set using the ANKLET_GLOBAL_RECEIVER_SECRET env var too
plugins:
  - name: GITHUB_RECEIVER
    plugin: github_receiver
    hook_id: 489747753
    port: 54321
    # secret: 12345
    private_key: /Users/nathanpierce/veertuinc-anklet.2024-07-19.private-key.pem
    app_id: 949431
    installation_id: 52970581
    repo: anklet
    owner: veertuinc
    skip_redeliver: true
    # redeliver_hours: 24 # default is 24 hours
    #database:
    #  url: localhost
    #  port: 6379
    #  user: ""
    #  password: ""
    #  database: 0
```

- If you leave off `repo`, the receiver will be an organization level receiver.
- Note: The receiver must come FIRST in the `plugins:` list. Do not place it after other plugins.
- **IMPORTANT**: On first start, it will scan for failed webhook deliveries for the past 24 hours and send a re-delivery request for each one. This is to ensure that all webhooks are delivered and processed and nothing in your plugins are orphaned or database. Avoid excessive restarts or else you'll eat up your API limits quickly. You can use `skip_redeliver: true` to disable this behavior.

---

## API Endpoints

- `/jobs/v1/receiver` - This is the endpoint that Github will send the webhook to. This is where the receiver will receive the webhook and store it in the database.

## Webhook Trigger Setup

1. Find your repo in github.com
1. Click on `Settings`
1. Click on `Webhooks`
1. Click on `Add webhook`
1. Set the `Payload URL` to the Public IP or URL that points to the server running the Anklet Github Receiver + `/jobs/v1/receiver`. So for example: `http://99.153.180.48:54321/jobs/v1/receiver`
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
| service_status | Status of the service (idle, running, limit_paused, stopped) |
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
service_status{service_name=github_receiver,plugin=github_receiver,owner=veertuinc,repo=anklet} running
host_cpu_count 12
host_cpu_used_count 2
host_cpu_usage_percentage 17.010309
host_memory_total_bytes 38654705664
host_memory_used_bytes 19195559936
host_memory_available_bytes 19459145728
host_memory_usage_percentage 49.659051
host_disk_total_bytes 994662584320
host_disk_used_bytes 537421299712
host_disk_available_bytes 457241284608
host_disk_usage_percentage 54.030513
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
