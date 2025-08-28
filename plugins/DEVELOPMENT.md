# Plugin Development Guide

Plugins are the core of Anklet. They are split into two main categories:

- Receivers
- Handlers

## Receivers

Receivers are responsible for listening for events from your CI platform/tool and placing them in a queue (redis DB for example).

## Handlers

Are responsible for pulling jobs from the queue and executing them on a host + in an Anka VM.

---

Our github plugin is a good example of how to implement a receiver and handler. It was the first plugin we wrote and is a good starting point for new plugins.

## How to create a new plugin

1. Fork the repo.
1. Determine if you need a receiver or handler.
1. Create a new directory in the `plugins/[receiver|handler]/[your-plugin-name]` directory.
1. Create three files in the directory:
   1. `README.md`
   1. `plugin.go` (see example in `plugins/[receiver|handler]/github/plugin.go`)
   1. `[your-plugin-name].go`
1. Run `make generate-plugins` to update the `internal/plugins/plugins.go` file with the new import.

All supporting files should be in the same directory as the golang file. For example, the github actions plugin has bash scripts it injects into the VM to download, install, and register the github actions runner inside of the VM. Those are in the `plugins/handlers/github` directory.

## What to consider when coding a plugin

All of the functions you'll use should be in the `internal` directory. Here is a summary of each at the time of writing:

1. [`internal/anka`](../internal/anka) - Logic to interact with the Anka CLI and VM on the host.
1. [`internal/config`](../internal/config) - Structs and functions to load and interact with the config file.
1. [`internal/database`](../internal/database) - Functions to interact with the redis database.
1. [`internal/github`](../internal/github) - Functions to interact with the github API.
1. [`internal/host`](../internal/host) - Functions to interact with the host (get CPU, Memory, and Disk usage, etc.)
1. [`internal/logging`](../internal/logging) - Functions to log messages.
1. [`internal/metrics`](../internal/metrics) - Functions to interact with the metrics server.
1. [`internal/plugins`](../internal/plugins) - Plugin functions (no need to change)