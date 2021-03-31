# KCP: A control plane for Kube-like Applications

IMPORTANT: This is a prototype of a first-draft of a set of ideas - it is not production software, or a fully realized project. In the short term, it is to serve as a test bed for some opinionated multi-cluster concepts. Please explore and play, but don'tdepend on it.

KCP manages Kubernetes applications across one or more clusters. To an end user, KCP appears to be a normal cluster (supports the same APIs, client tools, and extensibility) but allows you to move your workloads between clusters or span multiple clusters without effort. KCP lets you keep your existing workflow and abstract Kube clusters like a Kube cluster abstracts individual machines.

## What does it do for me?

As a Kubernetes application author, KCP allows you to:

* Take existing Kubernetes applications and set them up to run across one or more clusters even if a cluster fails
* Set up a development workflow that uses existing Kubernetes tools but brings your diverse environments (local, dev, staging, production) together
* Run multiple applications side by side in **tenant clusters**

As a Kubernetes administrator, KCP allows you to:

* Support a large number of application teams building applications without giving them access to clusters
* Have strong tenant separation between different application teams and control who can run where
* Allow tenant teams to run their own custom resources (CRDs) and controllers without impacting others
* Subdivide access to the underlying clusters, keep those clusters simpler and with fewer extensions, and reduce the impact of cluster failure

As an author of Kubernetes extensions, KCP allows you to:

* Build multi-cluster integrations more easily by providing standard ways to abstract multi-cluster actions like placement/scheduling, admission, and recovery
* Test and run Kubernetes CRDs and controllers in isolation without needing a full cluster


As a Kubernetes community member, KCP is intended to:

* Solve problems that benefit both regular Kubernetes clusters and the standalone KCP control plane
* Improve low level tooling for client authors writing controllers across multiple namespaces and clusters


## Key ideas

* Use Kubernetes APIs to decouple desired intent and actual state for replicating applications to multiple clusters

* Virtualize some key user focused Kube APIs so that the control plane can delegate complexity to a target cluster

* Identify and invest in workload APIs and integrations that enable applications to spread across clusters

* Use logical tenant clusters as the basis for application and security isolation

Allow a single kube-apiserver to support multiple (up to 1000) logical clusters that can map/sync/schedule to zero or many physical clusters. Each logical cluster could be much more focused - only the resources needed to support a single application or team, but with the ability to scale to lots of applications. Because the logical clusters are served by the same server, we could amortize the cost of each individual cluster (things like RBAC, CRDs, and authentication can be shared / hierarchal).

By relying on cluster tenancy, we gain the ability to bring new isolation mechanisms that can be stronger than what is possible within an existing Kubernetes cluster, but we also can:

* Support multiple versions of APIs from multiple clusters cleanly, allowing tenancy

* 


## Inspired and influenced by

KCP explores many ideas that have been present in the Kubernetes community since the early days of the project. It attempts to address a number of existing challenges in a unified way and builds on the hard work and investments of many projects and individuals:

* Kubernetes Federation

kubefed (aka Ubernetes) began very early in the Kubernetes project lifecycle and explored creating a control plane that behaved like Kubernetes on top of Kube. However, much of the key infrastructure that would be required to lifecycle API objects across multiple Kubernetes clusters and versions did not exist, nor were our core APIs mature, nor did we have the ability to extend the API server. The key lesson KCP iterates on from kubefed is explicitly handling API versioning across multiple clusters, supporting CRDs explicitly even for core resources, and the ability to subdivide by tenant cluster instead of just namespaces.

* Kubernetes `virtual-cluster`

Work within the community over the years has shown that there are limits to tenancy within a single Kubernetes cluster. The virtual-cluster project within sig-multicluster and the work done by Hauwei and others was a step in this direction - what if we could run arbitrarily many clusters?  How can we make each of those more efficient?  What changes to Kubernetes itself would be necessary to better support lots of smaller clusters?  This key insight from virtual-cluster leads to an obvious question - can we make virtual-clusters even more efficient and more representative of a single app / team domain?

* 