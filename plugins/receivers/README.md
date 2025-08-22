# Anklet Receivers

The receivers are responsible for listening for events from your CI platform/tool and placing them in a queue (redis DB for example).

## How it works

1. The receiver listens for events from your CI platform/tool (defined by the plugin).
2. The receiver places the event in the queue (redis DB).

Once a job is placed in the queue, the [handler](../handlers) will pull it and execute the job. Once the job is complete, the Receiver will receive a completion event and place it in the queue for the handler to see and act on.

## What Receivers should NOT do

- They should not execute the job or any logic related to a VM (start, prep, cleanup, etc).
- They should not communicate back to the CI platform/tool. Though, this may be subject to change due to the nature of your tool. Generally, a receiver should only receive events and place them in the queue.

## What Receivers should do

- They should listen for events from your CI platform/tool.
- They should place the event in the queue for handlers to act on.