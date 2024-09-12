# Project Goals

`kcp`'s goal is to be a cloud native control plane for APIs that resemble IaaS/PaaS/SaaS patterns. The central motivation of building `kcp` is to be able to provide arbitrary services through a unified platform and resource model. `kcp` is not providing significant as-a-Service implementations but understands itself as a foundational piece of technology that can be built upon, whether it be a SaaS platform, an IdP (internal developer platform) or a multi-cluster scheduling solution. We aim to build an ecosystem that centers around a Kubernetes-like control plane (`kcp`) but services many scenarios that benefit from having a declarative data store.

!!! warning
    Previously, `kcp` had a bigger scope of providing a transparent multi-cluster (TMC) solution. These efforts have been put on hold for the time being. If you are interested in reviving them, please reach out to the maintainers.

## The Manifesto

Our mission is to improve building and running cloud-native services. We see a convergence in tooling and technology between clusters, clouds, and services as being both possible and desirable and `kcp` explores how the existing Kubernetes ecosystem might evolve to serve that need.

Not every idea below may bear fruit, but it's never the wrong time to look for new ways to change.


### Key Concepts

* Use Kubernetes APIs to decouple desired intent and actual state for replicating applications to multiple clusters

  Kubernetes' strength is separating user intent from actual state so that machines can ensure recovery as infrastructure changes. Since clusters are intended to be a single failure domain, by separating the desired state from any one "real" cluster we can potentially unlock better resiliency, simpler workload isolation, and allow workloads to move through the dev/stage/prod pipeline more cleanly. If we can keep familiar tools and APIs working, but separate the app just a bit from the cluster, that can help us move and react to failure more effectively.

* Use logical tenant clusters as the basis for application and security isolation

  Allow a single kube-apiserver to support multiple (up to 1000) logical clusters that can map/sync/schedule to zero or many physical clusters. Each logical cluster could be much more focused - only the resources needed to support a single application or team, but with the ability to scale to lots of applications. Because the logical clusters are served by the same server, we could amortize the cost of each individual cluster (things like RBAC, CRDs, and authentication can be shared / hierarchal).

  We took inspiration from the [virtual cluster project](https://github.com/kubernetes-sigs/multi-tenancy/tree/master/incubator/virtualcluster) within sig-multicluster as well as [vcluster](https://github.com/loft-sh/vcluster) and other similar approaches that leverage cluster tenancy which led us to ask if we could make those clusters an order of magnitude cheaper by building within the kube-apiserver rather than running full copies. Most applications are small, which means amortizing costs can become a huge win. Single process sharing would let us embed significantly more powerful tenancy concepts like hierarchy across clusters, virtualizing key interfaces, and a much more resilient admission chain than what can be done in webhooks.

  See the [investigations doc for logical clusters](./developers/investigations/logical-clusters.md) for more.

  Most importantly, if clusters are cheap, we can:

* Support stronger tenancy and isolation of CRDs and applications

  Lots of little clusters gives us the opportunity to improve how CRDs can be isolated (for development or individual teams), shared (one source for many consumers), and evolved (identify and flag incompatibilities between APIs provided by different clusters). A control plane above Kubernetes lets us separate the "data plane" of controllers/integrations from the infrastructure that runs them and allows for centralization of integrations. If you have higher level workloads, talking to higher level abstractions like cloud services, and the individual clusters are just a component, suddenly integrating new patterns and controls becomes more valuable. Conversely, if we have a control plane and a data plane, the types of integrations at each level can begin to differ. More powerful integrations to physical clusters might be run only by infrastructure operations teams, while application integrations could be safely namespaced within the control plane.

  Likewise, as we split up applications into smaller chunks, we can more carefully define their dependencies.  The account service from the identity team doesn't need to know the details of the frontend website or even where or how it runs. Instead, teams could have the freedom of their own personal clusters, with the extensions they need, without being able to access the details of their peer's except by explicit contract.

  If we can make extending Kubernetes more interesting by providing this higher level control plane, we likewise need to deal with the scalability of that extensibility:

* Make Kubernetes controllers more scalable and flexible on both the client and the server

  Subdividing one cluster into hundreds makes integrations harder - a controller would need to be able to access resources across all of those clusters (whether logical, virtual, or physical). For this model to work, we need to explore improvements to the Kubernetes API that would make multi-cluster controllers secure and easy. That involves ideas like watching multiple resources at the same time, listing or watching in bulk across lots of logical clusters, filtering server side, and better client tooling. Many of these improvements could also benefit single-cluster use cases and scalability.

  To go further, standardizing some of the multi-cluster concepts (whether scheduling, location, or resiliency) into widely used APIs could benefit everyone in the Kubernetes ecosystem, as we often end up building and rebuilding custom platform tooling. The best outcome would be small incremental improvements across the entire Kube ecosystem leading to increased reuse and a reduced need to invest in specific solutions, regardless of the level of the project.

  Finally, the bar is still high to writing controllers. Lowering the friction of automation and integration is in everyone's benefit - whether that's a bash script, a Terraform configuration, or custom SRE services.  If we can reduce the cost of both infrastructure as code and new infrastructure APIs we can potentially make operational investments more composable.

  See the [investigations doc for minimal API server](./developers/investigations/minimal-api-server.md) for more on 
  improving the composability of the Kube API server.


### Process

Right now we are interested in assessing how these goals fit within the larger ecosystem.

### Principles

Principles are the high level guiding rules we'd like to frame designs around. This is generally useful for resolving design debates by finding thematic connections that reinforce other choices. A few early principles have been discussed:

1. Convention over configuration / optimize for the user's benefit

    Do as much as possible for the user the "right way by default" (conventions over configuration). For example, `kcp` embeds the data store for local iteration, but still allows (should allow) remote etcd.

2. Support both a push model and a pull model that fit the control plane mindset

    Both push (control plane tells others what to do) and pull (agents derive truth from control plane) models have their place. Pull works well when pulling small amounts of desired state and when local resiliency is desired as well as to create a security boundary. Push works well in simple getting started scenarios and when the process is "acting on behalf" of a user. For example, `kcp` and the `cluster-controller` example in the demo can work in both the push model (talk to each cluster to grab CRDs and sync resources) and the pull model (run as a separate controller so that customized security rules could be in place). Users should have the ability to pick the right tradeoff for their scale and how their control planes are structured.

3. Balance between speed of local development AND running as a high scale service

    `kcp` should not overly bias towards "just the demo" (in the long run) or take explicit steps that would prevent it from being a highly scalable control-plane-as-a-service. The best outcome would be a simple tool that works at multiple scales and layers well.

4. Be simple, composable, and orthogonal

    The core Kubernetes model is made of simple composable resources (pods vs services, deployments vs replica sets, persistent volumes vs inline volumes) with a focus on solving a core use case well. `kcp` should look for the key composable, orthogonal, and "minimum-viable-simple" concepts that help people build control planes, support API driven infra across a wide footprint, and provides a center of gravity for "integrate all the things". It however should not be afraid to make specific sets of users happy in their daily workflow.

5. Be open to change

    There is a massive ecosystem of users, vendors, service providers, hackers, operators, developers, and machine AIs (maybe not the last one) building and developing on Kubernetes. This is a starting point, a stake in the ground, a rallying cry. It should challenge, excite, and inspire others, but never limit. As we evolve, we should stay open to new ideas and also opening the door for dramatic rethinks of the possibilities by ourselves or others. Whether this becomes a project, inspires many projects, or fails gloriously, it's about making our lives a bit easier and our tools a bit more reliable, and a meaningful dialogue with the real world is fundamental to success.

6. Consolidate efforts in the ecosystem into a more focused effort

    Kubernetes is mature and changes to the core happen slowly. By concentrating use cases among a number of participants we can better articulate common needs, focus the design time spent in the core project into a smaller set of efforts, and bring new investment into common shared problems strategically. We should make fast progress and be able to suggest high-impact changes without derailing other important Kubernetes initiatives.

