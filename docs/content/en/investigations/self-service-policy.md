---
description: >
  Improve consistency and reusability of self-service and policy enforcement across multiple Kubernetes clusters.
---

# Self-service policy

## Goal

Improve consistency and reusability of self-service and policy enforcement across multiple Kubernetes clusters.

> Just like Kubernetes standardized deploying containerized software onto a small set of machines, we want to standardize self-service of application focused integration across multiple teams with organizational control.

Or possibly

> Kubernetes standardized deploying applications into chunks of capacity.  We want to standardize isolating and integrating application teams across organizations, and to do that in a way that makes applications everywhere more secure.

## Problem

A key component of large Kubernetes clusters is shared use, where the usage pattern might vary from externally controlled (via gitops / existing operational tools) to a permissive self-service model.  The most common partitioning model in Kubernetes is namespace, and the second most common model is cluster.

Self-service is currently limited by the set of resources that are namespace scoped for the former, and by the need to parameterize and configure multiple clusters consistently for the latter. Cluster partitioning can uniquely offer distinct sets of APIs to consumers. Namespace partitioning is cheap up until the scale limits of the cluster (~10k namespaces), while cluster partitioning usually has a fixed cost per cluster in operational and resource usage, as well as lower total utilization.

Once a deployment reaches the scale limit of a single cluster, operators often need to redefine their policies and tools to work in a multi-cluster environment. Many large deployers create their own systems for managing self-service policy above their clusters and leverage individual subsystems within Kubernetes to accomplish those goals.

## Approach

The logical cluster concept offers an opportunity to allow self-service at a cluster scope, with the effective cost of the namespace partitioning scheme. In addition, the separation of workload at control plane (kcp) and data plane (physical cluster) via [transparent multi-cluster](transparent-multi-cluster.md) or similar schemes allows strong policy control of what configuration is allowed (reject early), restriction of the supported API surface area for workload APIs (limit / control certain fields like pod security), and limits the access of individual users to the underlying infra (much like clusters limit access to nodes).

It should be possible to accomplish current self-service namespace and cluster partitioning via the logical cluster mechanism + policy enforcement, and to incentivize a wider range of "external policy control" users to adopt self-service via stronger control points and desirable use cases (multi-cluster resiliency for apps).

We want to enable concrete points of injection of policy that are difficult today in Kubernetes tenancy:

1. The acquisition of a new logical cluster with **capabilities** and **constraints**
2. How the APIs in a logical cluster are **transformed** to an underlying cluster
3. How to manage the evolution of APIs available to a logical cluster over time
4. New hierarchal policy options are more practical since different logical clusters can have different APIs

## Areas of investigation

* Using logical clusters as a mechanism for tenancy, but having a backing implementation that can change
  * I.e. materialize logical clusters as an API resource in a separate logical cluster
  * Or implementing logical clusters outside the system and having the kcp server implementation be a shim
  * Formal "policy module" implementations that can be plugged into a minimal API server while using logical cluster impl
* Catalog the set of tenancy constructs in use in Kube
  * Draw heavily on sig-multitenancy explorations - work done by [cluster api nested](https://github.com/kubernetes-sigs/cluster-api-provider-nested/tree/main/virtualcluster), [virtual clusters](https://github.com/kubernetes-sigs/cluster-api-provider-nested/tree/main/virtualcluster), namespace tenancy, and [hierarchal namespace](https://github.com/kubernetes-sigs/hierarchical-namespaces) designs
  * Look at reference materials created by large organizational adopters of Kube
* Consider making "cost" a first class control concept alongside quota and RBAC (i.e. a service load balancer "costs" $1, whereas a regular service costs $0.001)
  * Could this more effectively limit user action
* Explore hierarchy of policy - if logical clusters are selectable by label, could you have composability of policy using controllers
* Explore using implicit resources
  * i.e. within a logical cluster have all resources of type RoleBinding be fetched from two sources - within the cluster, and in a separate logical cluster - and merged, so that you could change the global source and watches would still fire
  * Impliict resources have risk though - no way to "lock" them so the consequences of an implicit change can be expensive

## Progress

### Simple example of a policy implementation

Building out an example flow that goes from creating a logical cluster resource that results in a logical cluster being accessible to client, with potential hook points for deeper integration.

### Describe a complicated policy implementation

An example hosted multi-tenant service with billing, organizational policy, and tenancy isolation.
