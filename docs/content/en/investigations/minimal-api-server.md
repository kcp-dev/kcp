---
description: >
  Investigation on minimal API Server
---

# Minimal API Server

The Kubernetes API machinery provides a pattern for declarative config-driven API with a number of conventions that simplify building configuration loops and consolidating sources of truth. There have been many efforts to make that tooling more reusable and less dependent on the rest of the Kube concepts but without a strong use case driving separation and a design the tooling is still fairly coupled to Kube.

## Goal

Building and sustaining an API server that:

1. reuses much of the Kubernetes API server codebase to support Kube-like CRUD operations
2. adds, removes, or excludes some / any / all the built-in Kubernetes types
3. excludes some default assumptions of Kubernetes specific to the "Kube as a cluster" like "create the kubernetes default svc"
4. replaces / modifies some implementations like custom resources, backend storage (etcd vs others), RBAC, admission control, and other primitives

As a secondary goal, identifying where exceptions or undocumented assumptions exist in the libraries that would make clients behave differently generically (where an abstraction is not complete) should help ensure future clients can more concretely work across different API servers consistently.

### Constraint: The abstraction for a minimal API server should not hinder Kubernetes development

The primary consumer of the Kube API is Kubernetes - any abstraction that makes a standalone API server possible must not regress Kubernetes performance or overly complicate Kubernetes evolution. The abstraction *should* be an opportunity to improve interfaces within Kubernetes to decouple components and improve comprehension.

### Constraint: Reusing the existing code base

While it is certainly possible to rebuild all of Kube from scratch, a large amount of client tooling and benefit exists within patterns like declarative apply. This investigation is scoped to working within the context of improving the existing code and making the minimal changes within the bounds of API compatibility to broaden utility.

It should be possible to add an arbitrary set of the existing Kube resources to the minimal API server, up to and including what an existing kube-apiserver exposes. Several use cases desire RBAC, Namespaces, Secrets, or other parts of the workload, while ensuring a "pick and choose" mindset keeps the interface supporting the needs of the full Kubernetes server.

In the short term, it would not be a goal of this investigation to replace the underlying storage implementation `etcd`, but it should be possible to more easily inject the appropriate initialization code so that someone can easily start an API server that uses a different storage mechanism.

## Areas of investigation

1. Define high level user use cases
2. Document existing efforts inside and outside of the SIG process
3. Identify near-term SIG API-Machinery work that would benefit from additional decoupling (also wg-code-organization)
4. Find consensus points on near term changes and draft a KEP

## Use cases

### As a developer of CRDs / controllers / extensions

* I can launch a local Kube API and test out multiple different versions of the same CRD in parallel quickly (shared with [logical-clusters](logical-clusters.md))
* I can create a control plane for my organization's cloud resources (CRDs) that is centralized but doesn't require me to provision nodes (shared with [logical-clusters](logical-clusters.md))
* ... benefits for unit testing CRDs in controller projects?

### As a Kubernetes core developer

* The core API server libraries are better separated and more strongly reviewed
* Additional contributors are incentivized to maintain the core libraries of Kube because of a broader set of use cases
* Kube client tools have fewer edge cases because they are tested against multiple sets of resources
* ...

### As an aggregated API server developer

* It is easy to reuse the k8s.io/apiserver code base to provide:
  * A virtual read-only resource that proxies to another type
    * e.g. metrics-server
  * An end user facing resource backed by a CRD (editable only by admins) that has additional validation and transformation
    * e.g. service catalog
  * A subresource implementation for a core type (pod/logs) that is not embedded in the Kube apiserver code

### As a devops team

* I want to be able to create declarative APIs using the controller pattern ...
  * So that I can have declarative infrastructure without a full Kube cluster (<https://github.com/thetirefire/badidea> and <https://docs.google.com/presentation/d/1TfCrsBEgvyOQ1MGC7jBKTvyaelAYCZzl3udRjPlVmWg/edit#slide=id.g401c104a3c_0_0>)
  * So that I can have controllers that list/watch/sync/react to user focused changes
  * So that I can have a kubectl apply loop for my intent (spec) and see the current state (status)
  * So that I can move cloud infrastructure integrations like [AWS Controllers for k8s](https://github.com/aws-controllers-k8s/community) out of individual clusters into a centrally secured spot
* I want to be offer a "cluster-like" user experience to a Kube application author without exposing the cluster directly ([transparent multi-cluster](transparent-multi-cluster.md))
  * So that I can keep app authors from directly knowing about where the app runs for security / infrastructure abstraction
  * So that I can control where applications run across multiple clusters centrally
  * So that I can offer self-service provisioning at a higher level than namespace or cluster
* I want to consolidate all of my infrastructure and use gitops to talk to them the same way I do for clusters
  * ...

More detailed requests

* With some moderate boilerplate (50-100 lines of code) I can start a Kube compliant API server with (some / any of):
  * Only custom built-in types (code -> generic registry -> etcd)
  * CRDs (CustomResourceDefinition)
  * Aggregated API support (APIService)
  * Quotas and rate control and priority and fairness
  * A custom admission chain that does not depend on webhooks but is inline code
  * A different backend for storage other than etcd (projects like [kine](https://github.com/k3s-io/kine))
  * Add / wrap some HTTP handlers with middleware

## Progress

Initial work in k/k fork involved stripping out elements of kube-apiserver start that required "the full stack" or internal controllers such as the kubernetes.default.svc maintainer (roughly `pkg/master`). It also looked at how to pull a subset of Kube resources (namespaces, rbac, but not pods) from the core resource group. The `kcp` binary uses fairly normal `k8s.io/apiserver` methods to init the apiserver process.

Next steps include identifying example use cases and the interfaces they wish to customize in the control plane (see above) and then looking at how those could be composed in an approachable way. That also involves exploring what refactors and organization makes sense within the k/k project in concert with 1-2 sig-apimachinery members.
