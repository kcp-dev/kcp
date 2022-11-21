---
title: "Edge multi-cluster"
linkTitle: "Edge multi-cluster"
weight: 1
description: >
  Investigation of edge multi-cluster
---

Kubernetes offers a state-based management platform to declaratively developing and operating workloads. Kube allows a developer to
 create workloads that are node-agnostic and can be constrained to operate in places where certain conditions are met. Operators enjoy
  gaurantee that workloads are rescheduled to work wherever constraints allow them to run. This approach has satisfied the large
   majority of use cases in the cloud native community.

There is a growing community of cloud native developers and operators that work for organizations that require more fine-grain control
 over the distribution of workload due to the nature of their business. Telecommunications, industrial manufacturers, and automotive
  companies would like to rely on Kubernetes state-based management but they need more flexibility to accomodate for conditions like
   large-scale deployments, disconnected and autonomous operation, and constrained cluster size. These requirements represent the Edge 
   Multi-cluster use case.

A key area of investigation for `kcp` is exploring large-scale deployment of workloads that can operate under constraints and
 conditions unlike those accomodated by upstream Kubernetes. Ideally, we want declaritively defined workloads to operate across a
  hierarchy combining KCP workspaces and Kubernetes cluster namespaces to achieve greater scale and control over placement. Deployment
   of workloads across many thousands of destinations that include namespaced and non-namespaced objects, receiving state and reporting
    status (grouped and individually), require new interfaces based on (and extending) KCP primitives.

## Goal: Placement strategies

There are many different ways to characterize deployments of workloads in the Edge use case. Support for blue/green, canary, carbon
 intensity sensitive, and threat avoidance are some of the deployment strategies to consider when casting out 100's or 1000's of
  workloads to millions of destinations. Having the ability to assign workloads using a declaritively defined placement strategy to
   edge cluster locations, even if they are offline at times, is a key requirement of the edge multi-cluster use case.

## Overlap of roles, responsibilities, integrations, and security

Today's smartphone is not only the property of the user who purchased it. The reality is that application developers (app store),
 corporate entities (vpn profiles), and the smartphone manufacturer have roles and responsibilities on these devices. Edge cluster
  locations are no different in this respect. A scalable Edge multi-cluster solution can thrive and advance if there is support for
   community contribution powered by integration and proper security features. All of these considerations must work across an evolving
    hierarchy that combines small and large control plane API services.

## Hierarchy and Scale are core considerations

KCP has introduced the concept of transparent multi-cluster which can be used to expand the number of entry points for workloads and
 operations by reducing the size of the control plane to a single binary. Synchronization between KCP virtual workspaces and physical
  clusters has made it possible to deploy workloads centrally and then fan out to any defined subset of clusters. We would like to
   leverage multiple levels of hierarchy (cascading multiple instances of KCP and Kubernetes cluster) to arrive at a distributed system
    capable of reaching any targeted edge cluster with/without edge devices attached. Building out such a hierarchy will require a
     scalable database to hold the objects referenced by each of their associated API servers. The default database for KCP and
      Kubernetes is etcd. We will investigate alternatives to etcd distribution and replacement databases to increase scalability with
       sacrificing performance and resilience without negatively impacting cost.

## Goal: Modularity is key

KCP is designed in a way that promotes modularity. We intend to keep this key attribute as a core requirement. Allowing flexibility in
 hierarchy, deployment, integration, security, and inclusive of a variety of roles and responsibilies matrices is a requirement for the
  Edge multi-cluster use case.

<!-- ### Constraint: The workflows and practices teams use today should be minimally disrupted

Users typically only change their workflows when an improvement offers a significant multiplier. To be effective we must reduce
 friction (which reduces multipliers) and offer significant advantages to that workflow.

Tools, practices, user experiences, and automation should "just work" when applied to cluster-agnostic or cluster-aware workloads. This
 includes gitops, rich web interfaces, `kubectl`, etc. That implies that a "cluster" and a "Kube API" is our key target, and that we
  must preserve a majority of semantic meaning of existing APIs.

### Constraint: 95% of workloads should "just work" when `kubectl apply`d to `kcp`

It continues to be possible to build different abstractions on top of Kube, but existing workloads are what really benefit users. They
 have chosen the Kube abstractions deliberately because they are general purpose - rather than describe a completely new system we
  believe it is more effective to uplevel these existing apps.  That means that existing primitives like Service, Deployment,
   PersistentVolumeClaim, StatefulSet must all require no changes to move from single-cluster to multi-cluster

By choosing this constraint, we also accept that we will have to be opinionated on making the underlying clusters consistent, and we
 will have to limit / constrain certain behaviors. Ideally, we focus on preserving the user's view of the changes on a logical cluster,
  while making the workloads on a physical cluster look more consistent for infrastructure admins. This implies we need to explore both
   what these workloads might look like (a review of applications) and describe the points of control / abstraction between levels. -->

## Areas of investigation

1. Define the high level user Edge multicluster use case
2. Explore characteristics of most common Kube workload objects that could allow them to be placed in edge cluster locations
<!-- 4. Identify the control points and data flow between workload and physical cluster that would be generally useful across a wide 
range of approaches - such as:
    1. How placement is assigned, altered, and removed ("scheduling" or "placement")
    2. How workloads are transformed from high level to low level and then summarized back
    3. Categorize approaches in the ecosystem and gaps where collaboration could improve velocity
    4.
5. Identify key infrastructure characteristics for multi-cluster
    1. Networking between components and transparency of location to movement
    2. Data movement, placement, and replication
    3. Abstraction/interception of off-cluster dependencies (external to the system)
    4. Consistency of infrastructure (where does Kube not sufficiently drive operational consistency) -->
6. Seek consensus in user communities on whether the interfaces are practical
8. Formalize parts of the prototype into project(s) drawing on the elements above if successful!

## Use cases

<!-- Representing feedback from a number of multi-cluster users with a diverse set of technologies in play: -->

### As a user

<!-- 1. I can `kubectl apply` a workload that is agnostic to node placement to `kcp` and see the workload assigned to real resources
 and start running and the status summarized back to me.
2. I can move an application (defined in 1) between two physical clusters by changing a single high level attribute
3. As a user when I move an application (as defined in 2) no disruption of internal or external traffic is visible to my consumers
4. As a user I can debug my application in a familiar manner regardless of cluster
5. As a user with a stateful application by persistent volumes can move / replicate / be shared across clusters in a manner consistent
 with my storage type (read-write-one / read-write-many). -->

### As an infrastructure admin

<!-- 1. I can decommision an physical cluster and see workloads moved without disruption
2. I can set capacity bounds that control admission to a particular cluster and react to workload growth organically -->

## Progress

<!-- In the early prototype stage `kcp` uses the `syncer` and the `deployment-splitter` as stand-ins for more complex scheduling and
 transformation. This section should see more updates in the near term as we move beyond areas 1-2 (use cases and ecosystem research)
  -->

### Possible design simplifications

<!-- 1. Focus on every object having an annotation saying which clusters it is targeted at
   * We can control the annotation via admission eventually, works for all objects
   * Tracking declarative and atomic state change (NONE -> A, A->(A,B), A->NONE) on
     objects
2. RBAC stays at the higher level and applies to the logical clusters, is not synced
   * Implication is that controllers won't be syncable today, BUT that's ok because it's likely giving workloads control over the
    underlying cluster is a non-goal to start and would have to be explicit opt-in by admin
   * Controllers already need to separate input from output - most controllers assume they're the same (but things like service load
    balancer)
3. Think of scheduling as a policy at global, per logical cluster, and optionally the namespace (policy of object type -> 0..N clusters)
   * Simplification over doing a bunch of per object work, since we want to be transparent (per object is a future optimization with
    some limits) -->
