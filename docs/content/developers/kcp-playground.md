---
linkTitle: "kcp playground"
description: >
  Alternative approach for testing KCP
---

# KCP playground

The kcp playground is a kubectl plugin that allows users/developers to spin un playgrounds with kcp, pclusters, 
and an initial setup defined in a config file.

## Example playgrounds 

The easiest way to start using the playground is using example configurations show-casing kcp features by
reproducing tutorials already presented in this site and/or scenarios from kcp E2E tests.

### APIBinding and APIExports

This playground configuration show cases publishing APIs as a service provider,
as described in https://docs.kcp.io/kcp/main/en/concepts/quickstart-tenancy-and-apis/.

Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/apis/cowboy-api.yaml
```

This command gives you a playground with kcp already set-up with two workspaces: 
The `root:my-org:service-provider` workspace has an APIExport, offering a simple `Cowboy` API for
other workspaces to use.
The `root:my-org:applications` workspace has an APIBinding, allowing you to create `Cowboy` custom CRs.

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f - <<EOF
  apiVersion: wildwest.dev/v1alpha1
  kind: Cowboy
  metadata:
    name: woody
  spec: {}
EOF
```

Take your time to look at the APIBinding, the APIExport and the APIResourceSchema to understand how
those kcp primitives works.

TIPS: The playground config file contains some notes that could help you.

### East - West placement

This playground configuration show case deploying workloads on KCP,
showing what happens if a workload is scheduled to multiple locations.

Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/placement/east-west-placement.yaml
```

This gives you a playground with two kind clusters and kcp already set-up with two workspaces:
- The `root:my-org:infrastructure` workspace has two SyncTargets, one for each kind cluster, and two Locations,
  one selecting the east SyncTarget, the other the west SyncTarget.
- The `root:my-org:application` workspace has two Placement rules, one selecting the east location, one the west 
  location; with this configuration, workloads deployed on this workspace are going to be placed on both locations.

Deploy a workload in KCP:

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f test/e2e/reconciler/deployment/workloads/deployment.yaml
kubectl get deploy -A
```

Check workload have been deployed on one (and only one) compute cluster:

```shell
kubectl kcp playground use pcluster [us-east1|us-west1]
kubectl get deploy -A
```

Take your time to look at this setup to understand how workload placement is implemented in kcp; if you want
you can also change the config file and enable the deploymentCoordinator so your workloads are going to be
spread across the two locations.

### Pool of clusters

This playground configuration show case deploying workloads on KCP,
showing what happens if a workload is assigned to a Location which is backed by more than
one compute cluster.
Use the following command to start the playground:

```shell
kubectl kcp playground start -f test/kubectl-kcp-playground/examples/placement/pool-of-clusters.yaml
```

This playground gives you a setup similar to the one described in the previous example, but
both the two kind clusters are reference by a single Location/Placement rule, thus defining 
a pool of clusters; with this configuration, workloads deployed on this workspace are going
to be placed only one of the clusters in the pool.

Deploy a workload in KCP:

```shell
# From another terminal window
export KUBECONFIG=.kcp/playground.kubeconfig

kubectl kcp playground use shard main
kubectl ws root:my-org:applications
kubectl apply -f test/e2e/reconciler/deployment/workloads/deployment.yaml
kubectl get deploy -A
```

Check workload have been deployed on one (and only one) compute cluster:

```shell
kubectl kcp playground use pcluster [us-east1-cluster1|us-east1-cluster2]
kubectl get deploy -A
```

Take your time  to look at this setup to understand how workload placement is implemented in kcp.

## Build your own playgrounds

You can build your own playgrounds by starting from one of the above config files
or defining a new config from scratch using the playground config format described below.

```yaml
---
apiVersion: test.kcp.io/v1alpha1
kind: Playground
spec:
  # Shards to be created as part of the playground.
  # NOTE: As of today playground supports one shard only, and it must be named "main".
  shards:
    - name: main

      # Defines if a deployment coordinator has to be run for this Shard.
      deploymentCoordinator: true

      # Initial workspaces configuration for the Shard.
      # Each workspace can define its own type type as well as the configuration of a set of items
      # to be installed it il like syncTargets, locations, placements, apiResourceSchemas, apiExports
      # apiBindings and other resources.
      # Also, like in kcp, nested workspaces can be defined as well.
      # NOTE: As of today playground supports one top level workspace, and it must be named "root".
      # NOTE: Workspaces are created in same order they are defined in the config file.
      workspaces:
        - name: root # required

          # Type of the workspace (optional)
          type:
            name: "<workspace-type-name>"
            path: "<absolute reference to the workspace that owns this type>"

          # SyncTarget to be added to the workspace.
          # NOTE: Defining SyncTarget is equivalent to run `kubectl kcp workload sync`; the generated
          # syncer yaml will be applied to the target pCluster.
          # NOTE: The pCluster name must reference an entry in the corresponding section down in of this file.
          syncTargets:
            - name: "<SyncTarget name>" # required
              labels:
                "<key>": "<value>"
              pCluster: "<name of a pCluster>" # required
              resource:
                - "<kind>"
                - "..."
              apiExports:
                - path: "<absolute reference to the workspace that owns this APIExport>"
                  name: "<name of the APIExport>"
                - "..."
            # Add more syncTargets...

          # Location to be added to the workspace; each location selects one or more instance/syncTarget.
          locations:
            - name: "<Location name>" # required
              labels:
                "<key>": "<value>"
              instanceSelector:
                matchLabels:
                  "<key>": "<value>"
                matchExpressions:
                  - ...
            # Add more locations...

          # Placement to be added to the workspace; use placements to define where workloads should be scheduled.
          # NOTE: Defining SyncTarget is equivalent to run `kubectl kcp bind compute`.
          placements:
            - name: "<placement name>" # required
              locationWorkspace: "<absolute reference to the workspace where locations are defined>" # required
              locationSelectors:
                - matchLabels:
                    "<key>": "<value>"
                  matchExpressions:
                    - ...
                - "..."
            # Add more placements...

          # APIResourceSchema to be added to the workspace; it is generated from a
          # source that can be either a CustomResource or APIResourceSchema.
          # NOTE: The conversion from CRD to APIResourceSchema is equivalent to run `kubectl crd snapshot`;
          # NOTE: More than one APIResourceSchema could be generated from an APIResourceSchema, but
          # kcp playground will automatically consider the entire set of generated objects when this
          # apiResourceSchema is referenced from APIExports objects.
          apiResourceSchemas:
            - name: "<apiResourceSchema name>" # required
              source: # required with one of raw and file
                raw: "<raw source of the APIResourceSchema>"
                file: # one of path of url
                  path: "<path to the source of the APIResourceSchema>"
                  url: "<url to the source of the APIResourceSchema>"
                prefix: "<prefix to be use if generating APIResourceSchema from a CRD>"
            # Add more apiResourceSchemas...

          # APIExport to be added to the workspace.
          apiExports:
            - name: "<apiExport name>" # required
              apiResourceSchemas: # required
                - "<apiResourceSchema name>"
            # Add more apiExports...

          # APIBinding to be added to the workspace.
          apiBindings:
            - name: "<apiBinding name>" # required
              apiExport:
                - path: "<absolute reference to the workspace that owns this APIExport>"
                  name: "<name of the APIExport>"
            # Add more apiBindings...

          # Additional resources to be added to the PCluster.
          others:
            - name: "<other resource name>" # required
              source: # required with one of raw and file
                raw: "<raw source of the resource>"
                file: # one of path of url
                  path: "<path to the source of the resource>"
                  url: "<url to the source of the resource>"
            # Add more resources...

          # Nested workspaces.
          # Like the root workspace, each Nested workspace can have type, syncTargets, locations, placements,
          # apiResourceSchemas, apiExports apiBindings, other resources and further nested workspaces
          workspaces:
            - "<nested workspace definition>"
            - "..."

  # Add compute clusters to the playground.
  # NOTE: currently only pCluster of type 'kind' are supported.
  pClusters:
    - name: "<name of the pCluster>"
      type: "kind"

      # Other resources to be added to the pCluster.
      others:
        - name: "<name of the resource>"
          source: # required with one of raw and file
            raw: "<raw source of the resource>"
            file: # one of path of url
              path: "<path to the source of the resource>"
              url: "<url to the source of the resource>"
      # Add more resources...

    # Add more pClusters...
```
