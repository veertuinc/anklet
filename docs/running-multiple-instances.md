# Running Multiple Anklet Instances on the Same Host

You can run multiple Anklet instances on the same host with different configurations. This is useful when you need to service multiple GitHub organizations, each with their own receiver and handler setup.

## Starting Multiple Instances

Use the `-c` flag to specify different config files for each instance:

```bash
./anklet -c ~/.config/anklet/config-org1.yml
./anklet -c ~/.config/anklet/config-org2.yml
```

## Configuration Requirements

When running multiple instances, each must have:

1. **Unique plugin names** - Plugin names must be unique across your entire Anklet cluster
2. **Different metrics ports** - Each instance needs its own metrics endpoint
3. **Different receiver ports** - Each receiver needs its own port for webhook ingestion

## Example: Two Organizations with Separate Receivers

### Organization 1 Configuration

```yaml
# config-org1.yml
---
global_database_url: localhost
global_database_port: 6379
global_receiver_secret: org1-webhook-secret

metrics:
  port: "8080"

plugins:
  - name: RECEIVER_ORG1
    plugin: github_receiver
    port: 54321
    hook_id: 1234567
    owner: org1
    private_key: ~/.config/anklet/org1-private-key.pem
    app_id: 111111
    installation_id: 11111111

  - name: HANDLER_ORG1
    plugin: github
    owner: org1
    private_key: ~/.config/anklet/org1-private-key.pem
    app_id: 111111
    installation_id: 11111111
```

### Organization 2 Configuration

```yaml
# config-org2.yml
---
global_database_url: localhost
global_database_port: 6379
global_receiver_secret: org2-webhook-secret

metrics:
  port: "8081"

plugins:
  - name: RECEIVER_ORG2
    plugin: github_receiver
    port: 54322
    hook_id: 7654321
    owner: org2
    private_key: ~/.config/anklet/org2-private-key.pem
    app_id: 222222
    installation_id: 22222222

  - name: HANDLER_ORG2
    plugin: github
    owner: org2
    private_key: ~/.config/anklet/org2-private-key.pem
    app_id: 222222
    installation_id: 22222222
```

## Webhook Setup

Each organization needs its own webhook configured in GitHub pointing to its respective receiver port:

- **Org 1**: `http://your-host:54321/jobs/v1/receiver`
- **Org 2**: `http://your-host:54322/jobs/v1/receiver`

## Shared Resources

### Redis Database

Both instances can share the same Redis database. However, by default, job queues are namespaced by owner (e.g., `anklet/jobs/github/queued/org1` vs `anklet/jobs/github/queued/org2`), so there are no conflicts.

### Anka CLI

Multiple Anklet instances interact with the same Anka installation. This works correctly because:

- The TemplateTracker coordinates template pulls to prevent corruption
- Each VM has a unique name based on the job

## Shared Queue for Multiple Organizations

If you want multiple organizations to share a single job queue (so handlers can process jobs from any org), use the `queue_name` option to override the default owner-based queue namespace:

```yaml
# config-org1.yml
plugins:
  - name: RECEIVER_ORG1
    plugin: github_receiver
    port: 54321
    owner: org1
    queue_name: shared_queue  # Both orgs write to the same queue
    # ... other config

  - name: HANDLER_SHARED
    plugin: github
    owner: org1
    queue_name: shared_queue
    # ... other config
```

```yaml
# config-org2.yml
plugins:
  - name: RECEIVER_ORG2
    plugin: github_receiver
    port: 54322
    owner: org2
    queue_name: shared_queue  # Both orgs write to the same queue
    # ... other config
```

With this configuration:
- Both receivers write jobs to `anklet/jobs/github/queued/shared_queue`
- Any handler configured with `queue_name: shared_queue` can pick up jobs from either organization
- The `owner` field is still used for GitHub API calls (authentication, runner registration)

## Optional: Additional Separation

For complete isolation, you can also separate:

```yaml
# Separate log files
log:
  file_dir: /var/log/anklet-org1/

# Separate plugin state directories
plugins_path: ~/.config/anklet/plugins-org1/

# Separate work directories
work_dir: /tmp/anklet-org1/

# Separate Redis databases (if desired)
global_database_database: 0  # Use 1 for org2
```

## Running as Services

When running as system services, create separate service files for each instance, each specifying its own config file path using the `-c` flag.
