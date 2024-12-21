# Mounts reconciler

Mounts reconciles the mount in the specific workspace and does the following:

- for now if it find secret in store - sets it ready


## VCluster controller

Order and flow:
1. `Finalizer` and sets finaziler and stop & requeue if change was done. (metadata change)
2. `Delegated/Direct` and sets `mountsv1alpha1.ClusterSecretReady,` (status change) and passthrough.
3. `Provisioner` and sets `mountsv1alpha1.ClusterReady,` (status change) and passthrough.
4. `Deprovisioner` and sets `mountsv1alpha1.ClusterNotReady,` (status change) and passthrough.
