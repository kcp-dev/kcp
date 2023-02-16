# Project Goals

!!! warning
    This is a prototype!  It is not production software, or a fully realized project with a definite road map. In the short term, it is to serve as a test bed for some opinionated multi-cluster concepts. This document describes the aspirations and inspirations and is written in a "this is what we could do" style, not "what we do today".

`kcp` can be used to manage Kubernetes-like applications across one or more clusters and integrate with cloud services. To an end user, `kcp` should appear to be a normal cluster (supports the same APIs, client tools, and extensibility) but allows you to move your workloads between clusters or span multiple clusters without effort. `kcp` lets you keep your existing workflow and abstract Kube clusters like a Kube cluster abstracts individual machines. `kcp` also helps broaden the definition of "Kubernetes applications" by being extensible, only loosely dependent on nodes, pods, or clusters, and thinking more broadly about what an application is than "just some containers".

## What should it do for me?

### 1-3 years

As an ecosystem participant, `kcp` is a reusable component that allows you to:

* Build your own secure control planes

As an application team, `kcp` allows you to:

* Deploy services, serverless applications, and containers side by side using familiar declarative config tooling from the Kubernetes ecosystem
* Go from the very small (laptop) to the very large (deployed around the world) without changing your development workflow

As an application infrastructure team, `kcp` allows you to:

* Define how your application teams work and integrate across machines, clusters, clouds, and environments without having to switch context
* Provide the tools for keeping your services resilient, observable, up-to-date, and profitable across any computing environment you choose to leverage

These first two areas are deliberately broad - they reflect where we think we as an ecosystem should be going even if we may not get there in one step, and to frame what we think is important for the ecosystem.


### 3-12 months

More pragmatically, we think the Kubernetes ecosystem is a great place to start from and so these are the kinds of incremental improvements from where we are today towards that aspirational future:

As a Kubernetes application author, `kcp` allows you to:

* Take existing Kubernetes applications and set them up to run across one or more clusters even if a cluster fails
* Set up a development workflow that uses existing Kubernetes tools but brings your diverse environments (local, dev, staging, production) together
* Run multiple applications side by side in **logical clusters**

As a Kubernetes administrator, `kcp` allows you to:

* Support a large number of application teams building applications without giving them access to clusters
* Have strong tenant separation between different application teams and control who can run where
* Allow tenant teams to run their own custom resources (CRDs) and controllers without impacting others
* Subdivide access to the underlying clusters, keep those clusters simpler and with fewer extensions, and reduce the impact of cluster failure

As an author of Kubernetes extensions, `kcp` allows you to:

* Build multi-cluster integrations more easily by providing standard ways to abstract multi-cluster actions like placement/scheduling, admission, and recovery
* Test and run Kubernetes CRDs and controllers in isolation without needing a full cluster

As a Kubernetes community member, `kcp` is intended to:

* Solve problems that benefit both regular Kubernetes clusters and standalone `kcp`
* Improve low level tooling for client authors writing controllers across multiple namespaces and clusters
* Be a reusable building block for ambitious control-plane-for-my-apps platforms

## The Manifesto

Our mission is to improve building and running cloud-native applications. We see a convergence in tooling and technology between clusters, clouds, and services as being both possible and desirable and this prototype explores how the existing Kubernetes ecosystem might evolve to serve that need.

Not every idea below may bear fruit, but it's never the wrong time to look for new ways to change.


### Key Concepts

* Use Kubernetes APIs to decouple desired intent and actual state for replicating applications to multiple clusters

  Kubernetes' strength is separating user intent from actual state so that machines can ensure recovery as infrastructure changes. Since clusters are intended to be a single failure domain, by separating the desired state from any one "real" cluster we can potentially unlock better resiliency, simpler workload isolation, and allow workloads to move through the dev/stage/prod pipeline more cleanly. If we can keep familiar tools and APIs working, but separate the app just a bit from the cluster, that can help us move and react to failure more effectively.

* Virtualize some key user focused Kube APIs so that the control plane can delegate complexity to a target cluster

  The Kubernetes APIs layer on top of each other and compose loosely. Some concepts like `Deployments` are well suited for delegation because they are self-contained - the spec describes the goal and status summarizes whether the goal is reached. The same goes for a `PersistentVolumeClaim` - you ask for storage and it follows your pod around a cluster - you don't really care about the details. On the other hand, you definitely need to get `Pod` logs to debug problems, and `Services` have a lot of cluster specific meaning (like DNS and the cluster IP). To scale, we need to let the real clusters focus on keeping the workload running, and keep the control plane at a higher level, and that may require us to pretend to have pods on the control plane while actually delegating to the underlying cluster.

* Identify and invest in workload APIs and integrations that enable applications to spread across clusters transparently

  Multi-cluster workload scheduling and placement has a rich history within Kubernetes from the very beginning of the project, starting with Kubernetes [federation v1](https://github.com/kubernetes-retired/federation).  Even today, projects like [karmada](https://github.com/karmada-io/karmada) are exploring how to take Kube APIs and make them work across multiple clusters. We want to amplify their ideas by improving the control plane itself - make it easy to plug in a workload orchestration system above Kube that still feels like Kube, without having a pesky cluster sitting around.

  See the [investigations doc for transparent multi-cluster](investigations/transparent-multi-cluster.md) for 
  more.

* Use logical tenant clusters as the basis for application and security isolation

  Allow a single kube-apiserver to support multiple (up to 1000) logical clusters that can map/sync/schedule to zero or many physical clusters. Each logical cluster could be much more focused - only the resources needed to support a single application or team, but with the ability to scale to lots of applications. Because the logical clusters are served by the same server, we could amortize the cost of each individual cluster (things like RBAC, CRDs, and authentication can be shared / hierarchal).

  We took inspiration from the [virtual cluster project](https://github.com/kubernetes-sigs/multi-tenancy/tree/master/incubator/virtualcluster) within sig-multicluster as well as [vcluster](https://github.com/loft-sh/vcluster) and other similar approaches that leverage cluster tenancy which led us to ask if we could make those clusters an order of magnitude cheaper by building within the kube-apiserver rather than running full copies. Most applications are small, which means amortizing costs can become a huge win. Single process sharing would let us embed significantly more powerful tenancy concepts like hierarchy across clusters, virtualizing key interfaces, and a much more resilient admission chain than what can be done in webhooks.

  See the [investigations doc for logical clusters](investigations/logical-clusters.md) for more.

  Most importantly, if clusters are cheap, we can:

* Support stronger tenancy and isolation of CRDs and applications

  Lots of little clusters gives us the opportunity to improve how CRDs can be isolated (for development or individual teams), shared (one source for many consumers), and evolved (identify and flag incompatibilities between APIs provided by different clusters). A control plane above Kubernetes lets us separate the "data plane" of controllers/integrations from the infrastructure that runs them and allows for centralization of integrations. If you have higher level workloads, talking to higher level abstractions like cloud services, and the individual clusters are just a component, suddenly integrating new patterns and controls becomes more valuable. Conversely, if we have a control plane and a data plane, the types of integrations at each level can begin to differ. More powerful integrations to physical clusters might be run only by infrastructure operations teams, while application integrations could be safely namespaced within the control plane.

  Likewise, as we split up applications into smaller chunks, we can more carefully define their dependencies.  The account service from the identity team doesn't need to know the details of the frontend website or even where or how it runs. Instead, teams could have the freedom of their own personal clusters, with the extensions they need, without being able to access the details of their peer's except by explicit contract.

  If we can make extending Kubernetes more interesting by providing this higher level control plane, we likewise need to deal with the scalability of that extensibility:

* Make Kubernetes controllers more scalable and flexible on both the client and the server

  Subdividing one cluster into hundreds makes integrations harder - a controller would need to be able to access resources across all of those clusters (whether logical, virtual, or physical). For this model to work, we need to explore improvements to the Kubernetes API that would make multi-cluster controllers secure and easy. That involves ideas like watching multiple resources at the same time, listing or watching in bulk across lots of logical clusters, filtering server side, and better client tooling. Many of these improvements could also benefit single-cluster use cases and scalability.

  To go further, standardizing some of the multi-cluster concepts (whether scheduling, location, or resiliency) into widely used APIs could benefit everyone in the Kubernetes ecosystem, as we often end up building and rebuilding custom platform tooling. The best outcome would be small incremental improvements across the entire Kube ecosystem leading to increased reuse and a reduced need to invest in specific solutions, regardless of the level of the project.

  Finally, the bar is still high to writing controllers. Lowering the friction of automation and integration is in everyone's benefit - whether that's a bash script, a Terraform configuration, or custom SRE services.  If we can reduce the cost of both infrastructure as code and new infrastructure APIs we can potentially make operational investments more composable.

  See the [investigations doc for minimal API server](investigations/minimal-api-server.md) for more on 
  improving the composability of the Kube API server.

* Drive new workload APIs and explore standardization in the ecosystem

  There are hundreds of ways to build and run applications, and that will never change. The key success of Kubernetes was offering "good enough" standardized deployment, which created a center of gravity for the concepts around deployment. There are plenty of deployments that will never run in containers yet consume them daily. Aligning the deployment of multiple types of workloads from common CI/CD tooling at a higher level, as well as abstracting their dependencies, is something in widespread practice today.

  Beyond deployment, we could look at connections between these applications (networking, security, identity, access) and find ways to bridge the operational divide between cloud and cluster. That might include expanding existing APIs like `PersistentVolumeClaims` so your data can follow you across clusters or services. Or documenting a selection of choices for multi-cluster networking that simplify assumptions apps need to make. Or even ways of connecting cluster and cloud resources more directly via unified identity, service meshes, and proxies (all of which are hot topics in our ecosystem).


### Process

Right now we are interested in assessing how these goals fit within the larger ecosystem.


### Terminology

We've attempted to pick novel terms for concepts introduced here so as not to conflict or confuse existing projects, but if you do spot problems let us know.

* **logical cluster** - a cluster that looks and acts like a Kube cluster but is not served by kube-apiserver (as distinct from virtual clusters in the upstream which are instances of kube-apiserver).
* **physical cluster** - a cluster with nodes, a kube-apiserver or equivalent tied to the standard APIs. Logical clusters might be indistingushable from physical clusters in some cases, but not always.
* **kcp the prototype** - where we are today
* **kcp the generic control plane** - a hypothetical future control plane leveraging kubernetes API tooling but not tied to kube the container orchestrator that can support diverse and interesting workloads
* **kcp the kube control plane** - a hypothetical future control plane for existing Kube applications that makes multi-cluster easy, superset of the generic control plane
* **kcp the extensible library** - a hypothetical golang library that can be embedded to make developing custom control planes easier


### Principles

Principles are the high level guiding rules we'd like to frame designs around. This is generally useful for resolving design debates by finding thematic connections that reinforce other choices. A few early principles have been discussed:

1. Convention over configuration / optimize for the user's benefit

    Do as much as possible for the user the "right way by default" (conventions over configuration). For example, `kcp` embeds the data store for local iteration, but still allows (should allow) remote etcd.

2. Support both a push model and a pull model that fit the control plane mindset

    Both push (control plane tells others what to do) and pull (agents derive truth from control plane) models have their place. Pull works well when pulling small amounts of desired state and when local resiliency is desired as well as to create a security boundary. Push works well in simple getting started scenarios and when the process is "acting on behalf" of a user. For example, `kcp` and the `cluster-controller` example in the demo can work in both the push model (talk to each cluster to grab CRDs and sync resources) and the pull model (run as a separate controller so that customized security rules could be in place). Users should have the ability to pick the right tradeoff for their scale and how their control planes are structured.

3. Balance between speed of local development AND running as a high scale service

    The prototype should not overly bias towards "just the demo" (in the long run) or take explicit steps that would prevent it from becoming a real project that could make control-plane-as-a-service possible in the future (in the short run). The best outcome would be a simple tool that works at multiple scales and layers well.

4. Be simple, composable, and orthogonal

    The core Kubernetes model is made of simple composable resources (pods vs services, deployments vs replica sets, persistent volumes vs inline volumes) with a focus on solving a core use case well. `kcp` should look for the key composable, orthogonal, and "minimum-viable-simple" concepts that help people build control planes, support API driven infra across a wide footprint, and provides a center of gravity for "integrate all the things". It however should not be afraid to make specific sets of users happy in their daily workflow.

5. Be open to change

    There is a massive ecosystem of users, vendors, service providers, hackers, operators, developers, and machine AIs (maybe not the last one) building and developing on Kubernetes. This is a starting point, a stake in the ground, a rallying cry. It should challenge, excite, and inspire others, but never limit. As we evolve, we should stay open to new ideas and also opening the door for dramatic rethinks of the possibilities by ourselves or others. Whether this becomes a project, inspires many projects, or fails gloriously, it's about making our lives a bit easier and our tools a bit more reliable, and a meaningful dialogue with the real world is fundamental to success.

6. Consolidate efforts in the ecosystem into a more focused effort

    Kubernetes is mature and changes to the core happen slowly. By concentrating use cases among a number of participants we can better articulate common needs, focus the design time spent in the core project into a smaller set of efforts, and bring new investment into common shared problems strategically. We should make fast progress and be able to suggest high-impact changes without derailing other important Kubernetes initiatives.

7. Make individual clusters transient / make multi-cluster as easy as multi-node

    Just like Kubernetes made multi-node use cases trivial for applications, multi-cluster use cases should be trivial with `kcp` (or at least, the transparent multi-cluster approach). That doesn't eliminate the need to have deep control, just clarifies it.
