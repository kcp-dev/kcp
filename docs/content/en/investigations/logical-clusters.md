---
description: >
  Investigation on logical clusters
---

# Logical clusters

Kubernetes evolved from and was influenced by earlier systems that had weaker internal tenancy than a general-purpose compute platform requires. The namespace, quota, admission, and RBAC concepts were all envisioned quite early in the project, but evolved with Kubernetes and not all impacts to future evolution were completely anticipated. Tenancy within clusters is handled at the resource and namespace level, and within a namespace there are a limited number of boundaries. Most organizations use either namespace or cluster separation as their primary unit of self service, with variations leveraging a rich ecosystem of tools.

The one concrete component that cannot be tenanted are API resources - from a Kubernetes perspective we have too much history and ecosystem to desire a change, and so intra-cluster tenancy will always be at its weakest when users desire to add diverse extensions and tools. In addition, controllers are practically limited to a cluster scope or a namespace scope by the design of our APIs and authorization models and so intra-cluster tenancy for extensions is particularly weak (you can't prevent an ingress controller from viewing "all secrets").

Our admission chain has historically been very powerful and allowed deep policy to be enforced for tenancy, but the lack of a dynamic plugin model in golang has limited us to what can be accomplished to external RPC webhooks which have a number of significant performance and reliability impacts (especially when coupled to the cluster the webhook is acting on). If we want to have larger numbers of tenants or stronger subdivision, we need to consider improving the scalability of our policy chain in a number of dimensions.

Ideally, as a community we could improve both namespace and cluster tenancy at the same time in a way that provides enhanced tools for teams and organizations, addresses extensions holistically, and improves the reliability and performance of policy control from our control planes.

## Goal: Getting a new cluster that allows a team to add extensions efficiently should be effectively zero cost

If a cluster is a desirable unit of tenancy, clusters should be amortized "free" and easy to operationalize as self-service. We have explored in the community a number of approaches that make new clusters cheaper (specifically [virtual clusters](https://github.com/kubernetes-sigs/multi-tenancy/tree/master/incubator/virtualcluster) in SIG-multi-tenancy, as well as the natural cloud vendor "as-a-service" options where they amortize the cost of many small clusters), but there are certain fundamental fixed costs that inflate the cost of those clusters. If we could make one more cluster the same cost as a namespace, we could dramatically improve isolation of teams as well as offering an advantage for more alignment on tenancy across the ecosystem.

### Constraint: A naive client should see no difference between a physical or logical cluster

A logical cluster that behaves differently from a physical cluster is not valuable for existing tools. We would need to lean heavily on our existing abstractions to ensure clients see no difference, and to focus on implementation options that avoid reduplicating a large amount of the Kube API surface area.

### Constraint: The implementation must improve isolation within a single process

As clusters grow larger or are used in more complex fashion, the failure modes of a single process API server have received significant attention within the last few years. To offer cheaper clusters, we'd have to also improve isolation between simultaneous clients and manage etcd usage, traffic, and both cpu and memory use at the control plane. These stronger controls would be beneficial to physical clusters and organizations running more complex clusters as well.

### Constraint: We should improve in-process options for policy

Early in Kubernetes we discussed our options for extension of the core capability - admission control and controllers are the two primary levers, with aggregated APIs being an escape hatch for more complex API behavior (including the ability to wrap existing APIs or CRDs). We should consider options that could reduce the cost of complex policy such as making using the Kube API more library-like (to enable forking) as well as in-process options for policy that could deliver order of magnitude higher reliability and performance than webhooks.

## Areas of investigation

1. Define high level user use cases
2. Prototype modelling the simplest option to enable `kcp` demo functionality and team subdivision
3. Explore client changes required to make multi-cluster controllers efficient
4. Support surfacing into a logical cluster API resources from another logical cluster
5. Layering RBAC so that changes in one logical cluster are additive to a source policy that is out of the logical cluster's control
6. Explore quota of requests, cpu, memory, and persistent to complement P&F per logical cluster with hard and soft limits
7. Explore making `kcp` usable as a library so that an extender could write Golang admission / hierarchal policy for logical clusters that reduces the need for external extension
8. Work through how a set of etcd objects could be moved to another etcd for sharding operations and keep clients unaware (similar to the "restore a cluster from backup" problem)
9. Explore providing a read only resource underlay from another logical cluster so that immutable default objects can be provided
10. Investigate use cases that would benefit even a single cluster (justify having this be a feature in kube-apiserver on by default)

## Use cases

The use cases of logical cluster can be seen to overlap heavily with [transparent multi-cluster](transparent-multi-cluster.md) use cases, and are captured at the highest level in [GOALS.md](../GOALS.md).  The use cases below attempt to focus on logical clusters independent of the broader goals.

### As a developer of CRDs / controllers / extensions

* I can launch a local Kube control plane and test out multiple different versions of the same CRD in parallel quickly
* I can create a control plane for my organization's cloud resources (CRDs) that is centralized but doesn't require me to provision nodes.

### As an infrastructure admin

* I can have strong tenant separation between different application teams
* Allow tenant teams to run their own custom resources (CRDs) and controllers without impacting others
* Subdivide access to the underlying clusters, keep those clusters simpler and with fewer extensions, and reduce the impact of cluster failure

### As a user on an existing Kubernetes cluster

* I can get a temporary space to test an extension before installing it
* I can create clusters that have my own namespaces

## Progress

### Logical clusters represented as a prefix to etcd

In the early prototype stage `kcp` has a series of patches that allow a header or API prefix path to alter the prefix used to retrieve resources from etcd. The set of available resources is stripped down to a minimal set of hardcoded APIs including namespaces, rbac, and crds by patching those out of kube-apiserver type registration.

The header `X-Kubernetes-Cluster` supports either a named logical cluster or the value `*`, or the prefix `/cluster/<name>` may be used at the root. This alters the behavior of a number of components, primarily retrieval and storage of API objects in etcd by adding a new segment to the etcd key (instead of `/<resource>/<namespace>/<name>`, `/<resource>/<cluster>/<namespace>/<name>`). Providing `*` is currently acting on watch to support watching resources across all clusters, which also has the side effect of populating the object `metadata.clusterName` field. If no logical cluster name is provided, the value `admin` is used (which behaves as a normal kube-apiserver would).

This means new logical clusters start off empty (no RBAC or CRD resources), which the `kcp` prototype mitigates by calculating the set of API resources available by merging from the default `admin` CRDs + the hardcoded APIs. That demonstrates one avenue of efficiency - a new logical cluster has an amortized cost near zero for both RBAC (no duplication of several hundred RBAC roles into the logical cluster) and API OpenAPI documents (built on demand as the union of another logical cluster and any CRDs added to the new cluster).

To LIST or WATCH these resources, the user specifies `*` as their cluster name which adjusts the key prefix to fetch all resources across all logical clusters. It's likely some intermediate step between client and server would be necessary to support "watch subset" efficiently, which would require work to better enable clients to recognize the need to relist as well as the need to make the prototype support some level of logical cluster subset retrieval besides just an etcd key prefix scan.

#### Next steps

* Continuing to explore how clients might query multiple resources across multiple logical clusters
* What changes to resource version are necessary to allow

### Zero configuration on startup in local dev

The `kcp` binary embeds `etcd` in a single node config and manages it for local iterative development. This is in keeping with optimize for local workflow, but can be replaced by connecting to an existing etcd instance (not currently implemented).  Ideally, a `kcp` like process would have minimal external dependencies and be capable of running in a shardable configuration efficiently (each shard handling 100k objects), with other components handling logical cluster sharding.

### CRD virtualization, inheritance, and normalization

A simple implementation of CRD virtualization (different logical clusters having different api resources), CRD inheritance (a logical cluster inheriting CRDs from a parent logical cluster), and CRD normalization (between multiple physical clusters) to find the lowest-common-denominator resource has been prototyped.

The CRD structure in the kube-apiserver is currently "up front" (as soon as a CRD is created it shows up in apiresources), but with the goal of reducing the up front cost of a logical cluster we may wish to suggest refactors upstream that would make the model more amenable to "on demand" construction and merging at runtime. OpenAPI merging is a very expensive part of the kube-apiserver historically (rapid CRD changes can have a massive memory and CPU impact) and this may be a logical area to invest to allow scaling within regular clusters.

Inheritance allows an admin to control which resources a client might use - this would be particularly useful in more opinionated platform flows for organizations that wish to offer only a subset of APIs. The simplest approach here is that all logical clusters inherit the admin virtual cluster (the default), but more complicated flows with policy and chaining should be possible.

Normalization involves reading OpenAPI docs from one or more child clusters, converting those to CRDs, finding the lowest compatible version of those CRDs (the version that shares all fields), and materializing those objects as CRDs in a logical cluster. This allows the minimum viable hook for turning a generic control plane into a spot where real Kube objects can run, and would be a key part of transparent multi-cluster.
