# GITHUB CONTROLLER PLUGIN

The Github Controller Plugin is used to receive webhook events from github and store them in the database for the Github Service Plugin to pick up and process.

### What you need:

1. An active database the controller can access. For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/blob/main/docs/database.md#database-setup).
1. Some sort of Auth method like a PAT or a Github App for the repo you want to receive webhooks about.
1. A way to receive webhooks. This can be a public URL or IP that points to the server running the Anklet Github Controller.

In the `config.yml`, you can define the `github_controller` plugin as follows:

```
services:
  - name: GITHUB_CONTROLLER
    plugin: github_controller
    hook_id: 489747753
    port: 54321
    secret: 123412342
    private_key: /Users/nathanpierce/veertuinc-anklet.2024-07-19.private-key.pem
    app_id: 949431
    installation_id: 52970581
    repo: anklet
    owner: veertuinc
    skip_redeliver: true
    database:
      enabled: true
      url: localhost
      port: 6379
      user: ""
      password: ""
      database: 0
```

- Note: The controller must come FIRST in the `services` list. Do not place it after other services.
- On first start, it will scan for failed webhook deliveries for the past 24 hours and send a re-delivery request for each one. This is to ensure that all webhooks are delivered and processed and nothing in your services are orphaned or database.

---

## API Endpoints

- `/jobs/v1/receiver` - This is the endpoint that Github will send the webhook to. This is where the controller will receive the webhook and store it in the database.

## Webhook Trigger Setup

1. Find your repo in github.com
1. Click on `Settings`
1. Click on `Webhooks`
1. Click on `Add webhook`
1. Set the `Payload URL` to the Public IP or URL that points to the server running the Anklet Github Controller + `/jobs/v1/receiver`. So for example: `http://99.153.180.48:54321/jobs/v1/receiver`
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