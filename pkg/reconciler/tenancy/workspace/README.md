# Workspace Controller

The workspace controller is root controller for the workspace.
It is responsible for (in calling order):

- `Metadata` - updates workspace metadata in annotations.
- `Delete` the workspace if it is in the `Deleting` phase and LogicalCluster finalizer is removed.
- `Scheduling` - picks the shard and schedules the workspace on the shard. Creates logical cluster for it.
- `Phase` - updates workspace phase based on the conditions of the workspace and LogicalCluster.
