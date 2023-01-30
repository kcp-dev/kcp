---
title: "Edge multi-cluster"
linkTitle: "Edge multi-cluster"
weight: 1
description: >
  Investigation of edge multi-cluster
---

Kubernetes offers a widely adopted, state-based management platform to declaratively develop and operate containerized workloads. Kubernetes allows a 
developer to create deployments that are node-agnostic and can be constrained to operate in places where certain conditions are met. Developers and 
Operators understand that pods will be rescheduled to execute on nodes wherever constraints allow them to run. This approach has satisfied the large
   majority of use cases in the cloud native community.

There is a growing community of cloud native developers and operators that require more fine-grained control
 over the distribution of pods due to the nature of their business. Telecommunications, industrial manufacturers, and automotive
  companies would like to rely on Kubernetes state-based management but they need more flexibility and specificity to accomodate for requirements like
  large numbers of edge locations, disconnected and autonomous operation, and constrained cluster size. In the edge multi-cluster usecase, there are
  situations when pods must run on a specific edge location on a specific node. Managing a set of labels, taints, and tolerations to cover all edge 
  locations is too difficult.

A key area of investigation for `kcp` is exploring large-scale deployment of workloads that can operate under constraints and
 requirements unlike those accomodated by upstream Kubernetes or KCP's Transparent MultiCluster (TMC). Ideally, we want declaritively defined workloads 
 to operate across a flexible topology combining KCP workspaces and Kubernetes cluster namespaces to achieve greater scale and control over placement. 
 Deployment of workloads across many thousands of destinations that include namespaced and non-namespaced objects, receiving state and reporting
  status (grouped and individually), require new interfaces based on (and extending) KCP primitives.

## Goal: Placement strategies

There are many different ways to characterize deployments of workloads in the Edge use case. Support for blue/green, canary, carbon
 intensity sensitive, and threat avoidance are some of the deployment strategies to consider when casting out 100's or 1000's of
  workloads to millions of destinations. Having the ability to assign workloads using a declaritively defined placement strategy to
   edge cluster locations, even if they are offline at times, is a key requirement of the edge multi-cluster use case.

## Overlap of roles, responsibilities, and security

Today's smartphone is not only the property of the user who purchased it. The reality is that application developers (app store),
 corporate entities (vpn profiles), and the smartphone manufacturer have roles and responsibilities on these devices. Edge cluster
  locations are no different in this respect. A scalable Edge multi-cluster solution can thrive and advance if there is support for
   community contribution powered by integration and proper security features. All of these considerations must work across an evolving
    hierarchy that combines small and large control plane API services.

## Hierarchy and Scale are core considerations

KCP has introduced the concept of a minimal API server which can be used to expand the number of places workloads can be deployed
  by reducing the size of the control plane to a single binary. Furthermore, KCP's Transparent Multi-cluster (TMC) has made it possible to define
  and deploy workloads centrally and then fan them out to specific edge locations. We would 
   like to leverage different topologies (for instance: cascading multiple instances of KCP and Kubernetes cluster) to arrive at a 
    distributed system capable of reaching any targeted edge cluster with/without edge devices attached. Topology flexibility 
     will require a scalable database to hold the objects referenced by each of their associated API servers. The default database for 
      KCP and Kubernetes is etcd. We will investigate alternatives to etcd distribution and replacement databases to increase 
       scalability without sacrificing performance and resilience and considering factors which could negatively impact cost.

## Areas of investigation

1. Define the high level user Edge multicluster use case
2. Explore characteristics of most common Kube workload objects that could allow them to be placed in edge cluster locations
3. Desired placement expression​: Need a way for one center object to express large number of desired copies​
4. Scheduling/syncing interface​: Need something that scales to large number of destinations​
5. Rollout control​: Client needs programmatic control of rollout, possibly including domain-specific logic​
6. Customization: Need a way for one pattern in the center to express how to customize for all the desired destinations​
7. Status from many destinations​: Center clients may need a way to access status from individual edge copies
8. Status summarization​: Client needs a way to say how statuses from edge copies are processed/reduced along the way from edge to center​.
9. Seek consensus in user communities on whether the interfaces are practical
10. Formalize parts of the prototype into project(s) drawing on the elements above if successful!

<!-- ## Use cases -->
<!-- placeholder for future -->
<!-- Representing feedback from a number of multi-cluster users with a diverse set of technologies in play: -->

### As a user

1. I can `kubectl apply` a workload that is agnostic to node placement to `kcp` and see the workload assigned to many edge location
 resources and start running and the status summarized back to me.
2. I can move an application (defined in 1) between two workload clusters by changing a single high level attribute
3. As a user when I move an application (as defined in 2) no disruption of internal or external traffic is visible to my consumers
4. As a user I can debug my application in a familiar manner regardless of cluster
5. As a user with a stateful application by persistent volumes can move / replicate / be shared across clusters in a manner consistent
 with my storage type (read-write-one / read-write-many).

## Progress

We are in the in the discovery and experimentation phase to learn what changes and additions are needed to KCP to support the edge multicluster use-case.  We are looking for partners and contributors to help us with validation and adoption.  Join us [here](https://calendar.google.com/calendar/embed?src=ujjomvk4fa9fgdaem32afgl7g0%40group.calendar.google.com) and watch recordings of our community meetings [here](https://www.youtube.com/playlist?list=PL1ALKGr_qZKc9jyv1EfOFNfoAJo9Q6Ebd).  Our next meeting is [here](https://github.com/kcp-dev/edge-mc/issues?q=is%3Aissue+is%3Aopen+label%3Acommunity-meeting)
