# TESTING PLUGINS

Plugins for Anklet must be tested individually in isolation. Veertu will provide the framework and the hardware to run the tests.

Once a review is performed by a team member, we will run the test suite manually to ensure that no insecure or damaging changes are introduced.

TODO: Add security scanning for all PRs.

## Framework

The framework includes a specifically formatted manifest that tells our testing system what hardware resources and then configuration is needed to run the tests.

Note: You won't need to specify the private key or credentials for the plugin. We will inject them into the config for you.

## Orientation

Under the root, we include `tests/plugins/` for each plugin. For example, `tests/plugins/github/` will run the tests against the code in `plugins/handlers/github/` and `plugins/receivers/github/`.

Inside of the `tests/plugins/github/` directory, there will be another directory for each test like `tests/plugins/github/test-basic/`. Inside of that directory, there will be a single `manifest.yaml` file and supporting scripts.

### manifest.yaml

This file contains the instructions that will be run against the plugin. Inside of it, you'll find a list of machines that will be used to run the test, their configuration, and the commands to run to trigger the tests to run.

#### Format

```yaml
description: "Github Actions basic tests"
tests:
    - name: "Starts Properly"
      hosts:
        - name: "receiver"
          id: "ubuntu-22.04-linux"
          config: |
            ---
            # Note: Don't specify any credentials for the plugin. We will inject them into the config for you.
            plugins:
              - name: GITHUB_RECEIVER1
                plugin: github_receiver
                redeliver_hours: 10

        - name: "handler-8-16"
          id: "13-L-ARM-macos"
          config: |
            ---
            # Note: Don't specify any credentials for the plugin. We will inject them into the config for you.
            plugins:
              - name: GITHUB_HANDLER1
                plugin: github

      steps:
        - name: "Trigger test run on 13-L-ARM-macos then watch the logs for the expected output"
          id: "13-L-ARM-macos"
          # working directory: tests/plugins/github/1-test-basic
          run: ./test.bash
```

This will set up the machines with anklet, the specific plugins, and then execute the `test.bash` script on the 13-L-ARM-macos machine. We won't go over the details of these scripts as they are specific to each test you want to perform.

