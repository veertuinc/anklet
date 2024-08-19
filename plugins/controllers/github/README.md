# GITHUB CONTROLLER PLUGIN

The Github Controller Plugin is used to receive webhook events from github and store them in the database for the Github Service Plugin to pick up and process.

For help setting up the database, see [Database Setup](https://github.com/veertuinc/anklet/blob/main/docs/database.md#database-setup).

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

## API LIMITS

The following logic consumes [API limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28). Should you run out, all processing will pause until the limits are reset after the specific github duration and then resume where it left off.
  - Requesting all hook deliveries for the past 24 hours.
  - To get verbose information for each hook delivery that's `in_progress` still.
  - Then again to post the redelivery request if all other conditions are met indicating it was orphaned.