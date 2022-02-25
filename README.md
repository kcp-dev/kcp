# `kcp` provides a Kubernetes-like control plane with true multitenancy, with flexible compute powered by real Kubernetes clusters.

![build status badge](https://github.com/kcp-dev/kcp/actions/workflows/ci.yaml/badge.svg)

`kcp` is a generic [CustomResourceDefinition](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/) (CRD) apiserver that is divided into multiple "[logical clusters](docs/investigations/logical-clusters.md)" that enable multitenancy of cluster-scoped resources such as CRDs and Namespaces. Each logical cluster is independent: the available APIs and data are separate from one logical cluster to another.

By default, `kcp` only knows about:

- [`Namespace`](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/)s
- [`ServiceAccount`](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/)s and [role-based access control](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) types like [`Role`](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#role-and-clusterrole) and [`RoleBinding`](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#rolebinding-and-clusterrolebinding)
- [`Secret`](https://kubernetes.io/docs/concepts/configuration/secret/)s and [`ConfigMap`](https://kubernetes.io/docs/concepts/configuration/configmap/)s, to store configuration data
- [`CustomResourceDefinition`](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)s, to define new types
- a handful of other low-level resources like [`Lease`](https://kubernetes.io/docs/reference/kubernetes-api/cluster-resources/lease-v1/)s, [`Event`](https://kubernetes.io/docs/tasks/debug-application-cluster/debug-application-introspection/)s, etc.

![kubectl api-resources showing kcp's API resources](./docs/images/kubectl-api-resources.png)

Any other resources, including standard Kubernetes resources like [`Deployment`](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)s and the rest, can be added as CRDs and optionally reconciled using the standard controllers launched against the `kcp` API, or via two-way replication into a Kubernetes cluster.


## Why would I want that?

Kubernetes is mainly known as a container orchestration platform today, but we believe it can be even more.

With the power of CRDs, Kubernetes provides a flexible platform for declarative APIs of _all types_, and the reconciliation pattern common to Kubernetes controllers is a powerful tool in building robust, expressive systems.

At the same time, a diverse and creative community of tools and services has sprung up around Kubernetes APIs.

Imagine a declarative Kubernetes-style API for _anything_, supported by an ecosystem of Kubernetes-aware tooling, separate from Kubernetes-the-container-orchestrator.

That's **`kcp`**.


## Can I start using `kcp` today?

Yes! This work is still in early development, which means it's _not ready for production_ and _APIs, commands, flags, etc. are subject to change_, but also that your feedback can have a big impact. Please try it out and [let us know](#this-sounds-cool-and-i-want-to-help) what you like, dislike, what works, what doesn't, etc.

`kcp` is currently a prototype, not a project. We're exploring these ideas here to try them out, experiment, and bounce them off each other.


## Can `kcp` do anything else?

Yes! Here are a few of our top-level goals:

### Host thousands (*) of small-ish Kubernetes-like [logical clusters](docs/investigations/logical-clusters.md) in a single instance

* Orders of magnitude fewer resources (~50) in a logical cluster compared to a typical multi-tenant Kubernetes cluster
* Inexpensive - nearly for free in terms of resource utilization & cost for an empty cluster
* Independent - install different versions of a CRD in each logical cluster (!!!)
* Per-cluster administrative rights - each "owner" (person/team/group) of a cluster is a full admin

### Treat compute as a utility
* [Transparent multi-cluster](docs/investigations/transparent-multi-cluster.md) - run `kcp` as a control plane in front of your physical compute clusters for workloads, and let `kcp` determine how to schedule your workloads to physical compute clusters
* Dynamically add more **compute** capacity as demand increases - not just nodes, but entire Kubernetes clusters

### Massive scale
* Model "organizations" as a way to group and manage "workspaces" (logical clusters). Support upwards of 10,000 organizations.
* 1,000,000 workspaces in a single installation
* Span across 1,000 shards (a shard is a `kcp` instance that holds a subset of organization and workspace content)
* This area is under active investigation. Stay tuned for more details!

### Local Kubernetes Development?

`kcp` could be _useful_ for local development scenarios, where you don't necessarily care about all of Kubernetes' many built-in resources and their reconciling controllers.

### Embedded/low-resource scenarios?

`kcp` could be _useful_ for environments where resources are scarce, by limiting the number of controllers that need to run. Kubernetes' asynchronous reconciliation pattern can also be very powerful in disconnected or intermittently connected environments, regardless of how workloads actually run.

## What about a long-term vision?

For more detailed information, check out our [GOALS.md](GOALS.md) doc and our [docs directory](docs/).


## Is `kcp` a "fork" of Kubernetes? 🍴

_No._

`kcp` as a prototype currently depends on some unmerged changes to Kubernetes, but we intend to pursue these changes through the usual KEP process, until (hopefully!) Kubernetes can be configured to run as `kcp` runs today.

Our intention is that our experiments _improve Kubernetes for everyone_, by improving [CRD](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)s and scaling resource watching, and enabling more, better controllers _for everyone_, whether you're using Kubernetes as a container orchestrator or not.

Our `kcp` specific patches in the [kcp-dev/kubernetes](https://github.com/kcp-dev/kubernetes) repo.

<!--
TODO, add docs on Kubernetes patches.
Old content:
See [DEVELOPMENT.md](DEVELOPMENT.md) for how the patches are structured and how they must be formatted during our experimentation phase.
-->

## What's in this repo?

- **`cmd`**
    - **`cluster-controller`**
        * Keeps track of physical `Cluster`s where workloads and other resources are synchronized
        * Enabled by default in `cmd/kcp`
    - **`compat`**
        * Checks compatibility between two CRDs and can generate the least common denominator CRD YAML if requested
    - **`crd-puller`**
        * Downloads CRDs from a cluster and writes them to YAML files
        * This functionality is included in parts of `cmd/kcp`
    - **`deployment-splitter`**
        * Splits a `Deployment` into multiple "virtual Deployments" across multiple physical clusters
    - **`kcp`**
        * The primary executable, which serves a Kubernetes-style API with a minimum of built-in types
    - **`kubectl-kcp`**
        * A kubectl plugin that offers kcp specific functionality
    - **`shard-proxy`**
        * An early experimental server that provides a workspace index and sharding details
    - **`syncer`**
        * Runs on Kubernetes clusters registered with the `cluster-controller`
        * Synchronizes resources in `kcp` assigned to the clusters
- **`cmd/virtual-workspaces`**
    * Demonstrates how to implement apiservers for custom access-patterns, e.g. like a workspace index.
- **`config`**:
    * Contains generated CRD YAML and helpers to bootstrap installing CRDs in `kcp`
- **`contrib`**:
    * CRDs for Kubernetes `apps` and `core` (empty group) types
    * Demo scripts
    * Examples
    * Local development utilities
- **`docs`**:
    * Our documentation
- **`hack`**:
    * Scripts and tools to support the development process
- **`pkg`**:
    * The majority of the code base
- **`test`**:
    * End to end tests
- **`third_party`**:
    * Code from third party projects for use in this repository


## What does `kcp` stand for?

`kcp` as a project stands for equality and justice for all people.

However, `kcp` is not an acronym.


## How do I get started?

1. Clone the repository.
2. [Install Go](https://golang.org/doc/install) (1.17+).
3. Download the [latest kubectl binary for your OS](https://kubernetes.io/docs/tasks/tools/#kubectl).
4. Build and start `kcp` in the background: `go run ./cmd/kcp start`.
5. Tell `kubectl` where to find the kubeconfig: `export KUBECONFIG=.kcp/admin.kubeconfig` (this assumes your working directory is the root directory of the repository).
6. Confirm you can connect to `kcp`: `kubectl api-resources`.

For more scenarios, see [DEVELOPMENT.md](DEVELOPMENT.md).


## This sounds cool and I want to help!

Thanks! And great! Please check out:

* [Good first issue](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22) issues
* [Help wanted](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22help+wanted%22) issues

You can also reach us here, in this repository via [issues](https://github.com/kcp-dev/kcp/issues) and [discussions](https://github.com/kcp-dev/kcp/discussions), or:

- Join the [`#kcp-prototype` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io)
- Join the mailing lists
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users
- Subscribe to the [community calendar](https://calendar.google.com/calendar/embed?src=ujjomvk4fa9fgdaem32afgl7g0%40group.calendar.google.com) for community meetings and events
    - The kcp-dev mailing list is subscribed to this calendar
- See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q)
- See [upcoming](https://github.com/kcp-dev/kcp/issues?q=is%3Aissue+is%3Aopen+label%3Acommunity-meeting) and [past](https://github.com/kcp-dev/kcp/issues?q=is%3Aissue+label%3Acommunity-meeting+is%3Aclosed) community meeting agendas and notes
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive


## References

- [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman (video)](https://www.youtube.com/watch?v=oaPBYUfdFE8)
- [OpenShift Commons: Kubernetes as the Control Plane for the Hybrid Cloud - Clayton Coleman (video)](https://www.youtube.com/watch?v=Y3Y11Aj_01I)
- [TGI Kubernetes 157: Exploring kcp: apiserver without Kubernetes](https://youtu.be/FD_kY3Ey2pI)
- [K8s SIG Architecture meeting discussing kcp, June 29 2021](https://www.youtube.com/watch?v=YrdAYoo-UQQ)
- [Let's Learn kcp - A minimal Kubernetes API server with Saiyam Pathak, July 7 2021](https://www.youtube.com/watch?v=M4mn_LlCyzk)
