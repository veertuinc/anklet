# Anklet Handlers

The handlers are responsible for pulling jobs from the queue, preparing a macOS VM, and registering the VM to the CI platform/tool so it can execute the job inside.

Handler generally work in tandem with a [Receiver](../receivers) plugin. It's not required, but it's recommended.

## How it works

Logic for each Handler can differ greatly depending on the plugin, but the general flow is:

1. Pull a job from the queue (redis DB).
2. Prepare a macOS VM with a specific template (defined by the user & plugin).
3. Register the VM to the CI platform/tool (defined by the plugin).
4. Wait for the job to complete.
5. Clean up the VM.

## What Handlers should NOT do

- They should not communicate with the Receiver.

## What Handlers should do

- They should pull jobs from the queue.
- They should prepare a VM.
- They should register the VM to the CI platform/tool.
- They should clean up the VM.
- Handle errors and, if needed, place the job back in the queue for other handlers to act on.
- Prevent collisions with other handlers. For example, two handlers shouldn't be pulling the same VM template/tag.