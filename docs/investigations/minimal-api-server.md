# Minimal API Server

The Kubernetes API machinery provides a pattern for declarative config-driven API with a number of conventions that simplify building configuration loops and consolidating sources of truth. There have been many efforts to make that tooling more reusable and less dependent on the rest of the Kube concepts but without a strong use case driving separation and a design the tooling is still fairly coupled to Kube.

## Goal: 

Running an API server that leverages the Kubernetes API server codebase, client libraries, and extension mechanisms without some or any of the built-in Kubernetes types either embedded or standalone. It is expected that most use cases would embed as a library vs be standalone.

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

### As a developer of CRDs / controllers / extensions ...

* I can launch a local Kube API and test out multiple different versions of the same CRD in parallel quickly (shared with [logical-clusters](logical-clusters.md))
* I can create a control plane for my organization's cloud resources (CRDs) that is centralized but doesn't require me to provision nodes (shared with [logical-clusters](logical-clusters.md))
* ... benefits for unit testing CRDs in controller projects?

### As a Kubernetes core developer ...

* The core API server libraries are better separated and more strongly reviewed
* Additional contributors are incentivized to maintain the core libraries of Kube because of a broader set of use cases
* Kube client tools have fewer edge cases because they are tested against multiple sets of resources
* ...

### As <project> ...

TODO: Fill with use cases from projects

* ...

## Progress

