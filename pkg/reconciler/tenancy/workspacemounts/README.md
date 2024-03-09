# Workspace mounts controller

Controller to manage workspace mounts for workspace. Workspace mounts are
external implementations of workspace mounts and are currently configured using
annotations on the workspace.

# TODO

1. Add an ability to change workspace status to not ready when a mount is not ready.

## Logic Overview

Controller has 2 queues, one for workspace mounts (gvk) and one for workspaces.
So it has 2 reconcilers loops running in parallel for each queue.

Overall controllers do not update any other resources, except for the workspace itself, annotation for now.
In the future, it will be updating status once we promote the status to be status and spec fields.

When the generic informer receives an event, checks if it's a mount and extracts
the workspace that owns it from annotation. Then controller enqueues the workspace object for reconciliation.

The Workspace reconciler checks if the workspace mount is already present in the system, and updates the the status of the workspace annotations with the mount status.
