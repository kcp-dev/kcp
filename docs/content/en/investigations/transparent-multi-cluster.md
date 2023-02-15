---
description: >
  Investigation of transparent multi-cluster
---

# Transparent multi-cluster

A key tenet of Kubernetes is that workload placement is node-agnostic until the user needs it to be - Kube offers a homogeneous compute surface that admins or app devs can "break-glass" and set constraints all the way down to writing software that deeply integrates with nodes. But for the majority of workloads a cluster is no more important than a node - it's a detail determined by some human or automated process.

A key area of investigation for `kcp` is exploring transparency of workloads to clusters. Aspirationally we want Kube workloads to be resilient to the operational characteristics of the underlying infrastructure and clusters orthogonally to the workload, by isolating the user from knowing of the details of the infrastructure. If workload APIs are more consistently "node-less" and "cluster-agnostic" that opens up ways to drive workload consistency across a large swathe of the compute landscape.

## Goal: The majority of applications and teams should have workflows where cluster is a detail

A number of projects have explored this since the beginning of Kubernetes - this prototype should explore in detail 
how we can make a normal Kubernetes flow for most users be cluster-independent but still "break-glass" and describe placement in detail. Since this is a broad topic, and we want to benefit the majority of users, we need to also add constraints that maximize the chance of these approaches being adopted.

### Constraint: The workflows and practices teams use today should be minimally disrupted

Users typically only change their workflows when an improvement offers a significant multiplier. To be effective we must reduce friction (which reduces multipliers) and offer significant advantages to that workflow.

Tools, practices, user experiences, and automation should "just work" when applied to cluster-agnostic or cluster-aware workloads. This includes gitops, rich web interfaces, `kubectl`, etc. That implies that a "cluster" and a "Kube API" is our key target, and that we must preserve a majority of semantic meaning of existing APIs.

### Constraint: 95% of workloads should "just work" when `kubectl apply`d to `kcp`

It continues to be possible to build different abstractions on top of Kube, but existing workloads are what really benefit users. They have chosen the Kube abstractions deliberately because they are general purpose - rather than describe a completely new system we believe it is more effective to uplevel these existing apps.  That means that existing primitives like Service, Deployment, PersistentVolumeClaim, StatefulSet must all require no changes to move from single-cluster to multi-cluster

By choosing this constraint, we also accept that we will have to be opinionated on making the underlying clusters consistent, and we will have to limit / constrain certain behaviors. Ideally, we focus on preserving the user's view of the changes on a logical cluster, while making the workloads on a physical cluster look more consistent for infrastructure admins. This implies we need to explore both what these workloads might look like (a review of applications) and describe the points of control / abstraction between levels.

### Constraint: 90% of application infrastructure controllers should be useful against `kcp`

A controller that performs app infra related functions should be useful without change against `kcp`. For instance, the etcd operator takes an `Etcd` cluster CRD and creates pods. It should be possible for that controller to target a `kcp` logical cluster with the CRD and create pods on the logical cluster that are transparently placed onto a cluster.

## Areas of investigation

1. Define high level user use cases
2. Study approaches from the ecosystem that do not require workloads to change significantly to spread
3. Explore characteristics of most common Kube workload objects that could allow them to be transparently placed
4. Identify the control points and data flow between workload and physical cluster that would be generally useful across a wide range of approaches - such as:
    1. How placement is assigned, altered, and removed ("scheduling" or "placement")
    2. How workloads are transformed from high level to low level and then summarized back
    3. Categorize approaches in the ecosystem and gaps where collaboration could improve velocity
    4.
5. Identify key infrastructure characteristics for multi-cluster
    1. Networking between components and transparency of location to movement
    2. Data movement, placement, and replication
    3. Abstraction/interception of off-cluster dependencies (external to the system)
    4. Consistency of infrastructure (where does Kube not sufficiently drive operational consistency)
6. Seek consensus in user communities on whether the abstractions are practical
7. Invest in key technologies in the appropriate projects
8. Formalize parts of the prototype into project(s) drawing on the elements above if successful!

## Use cases

Representing feedback from a number of multi-cluster users with a diverse set of technologies in play:

### As a user

1. I can `kubectl apply` a workload that is agnostic to node placement to `kcp` and see the workload assigned to real resources and start running and the status summarized back to me.
2. I can move an application (defined in 1) between two physical clusters by changing a single high level attribute
3. As a user when I move an application (as defined in 2) no disruption of internal or external traffic is visible to my consumers
4. As a user I can debug my application in a familiar manner regardless of cluster
5. As a user with a stateful application by persistent volumes can move / replicate / be shared across clusters in a manner consistent with my storage type (read-write-one / read-write-many).

### As an infrastructure admin

1. I can decommission a physical cluster and see workloads moved without disruption
2. I can set capacity bounds that control admission to a particular cluster and react to workload growth organically

## Progress

In the early prototype stage `kcp` uses the `syncer` and the `deployment-splitter` as stand-ins for more complex scheduling and transformation. This section should see more updates in the near term as we move beyond areas 1-2 (use cases and ecosystem research)

### Possible design simplifications

1. Focus on every object having an annotation saying which clusters it is targeted at
   * We can control the annotation via admission eventually, works for all objects
   * Tracking declarative and atomic state change (NONE -> A, A->(A,B), A->NONE) on
     objects
2. RBAC stays at the higher level and applies to the logical clusters, is not synced
   * Implication is that controllers won't be syncable today, BUT that's ok because it's likely giving workloads control over the underlying cluster is a non-goal to start and would have to be explicit opt-in by admin
   * Controllers already need to separate input from output - most controllers assume they're the same (but things like service load balancer)
3. Think of scheduling as a policy at global, per logical cluster, and optionally the namespace (policy of object type -> 0..N clusters)
   * Simplification over doing a bunch of per object work, since we want to be transparent (per object is a future optimization with some limits)
