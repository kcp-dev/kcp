<img alt="Logo" width="196px" align="left" src="./contrib/logo/blue-green.png"></img>

# `kcp` provides a true multi-tenant Kubernetes control plane for workloads on many clusters

`kcp` is a generic [CustomResourceDefinition](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/) (CRD) apiserver that is divided into multiple "[logical clusters](docs/investigations/logical-clusters.md)" that enable multitenancy of cluster-scoped resources such as CRDs and Namespaces. Each of these logical clusters is fully isolated from the others, allowing different teams, workloads, and use cases to live side by side.

<br clear="left"/>

![build status badge](https://github.com/kcp-dev/kcp/actions/workflows/ci.yaml/badge.svg)

By default, `kcp` only knows about:

- [`Namespace`](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/)s
- [`ServiceAccount`](https://kubernetes.io/docs/reference/access-authn-authz/service-accounts-admin/)s and [role-based access control](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) types like [`Role`](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#role-and-clusterrole) and [`RoleBinding`](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#rolebinding-and-clusterrolebinding)
- [`Secret`](https://kubernetes.io/docs/concepts/configuration/secret/)s and [`ConfigMap`](https://kubernetes.io/docs/concepts/configuration/configmap/)s, to store configuration data
- [`CustomResourceDefinition`](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)s, to define new types
- a handful of other low-level resources like [`Lease`](https://kubernetes.io/docs/reference/kubernetes-api/cluster-resources/lease-v1/)s, [`Event`](https://kubernetes.io/docs/tasks/debug-application-cluster/debug-application-introspection/)s, etc.

![kubectl api-resources showing kcp's API resources](./docs/images/kubectl-api-resources.png)

Any other resources, including standard Kubernetes resources like [`Deployment`](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/)s and the rest, can be added as CRDs and be replicated onto one or more Kubernetes clusters.


## Why would I want that?

Kubernetes is mainly known as a container orchestration platform today, but we believe it can be even more.

With the power of CRDs, Kubernetes provides a flexible platform for declarative APIs of _all types_, and the reconciliation pattern common to Kubernetes controllers is a powerful tool in building robust, expressive systems.

At the same time, a diverse and creative community of tools and services has sprung up around Kubernetes APIs.

Imagine a declarative Kubernetes-style API for _anything_, supported by an ecosystem of Kubernetes-aware tooling, separate from Kubernetes-the-container-orchestrator.

But even a generic control plane is only as good as the use cases it enables. We want to give existing Kubernetes users a new superpower - take your existing applications and allow them to move or spread across clusters without having to change a single line of YAML. We believe moving a workload between clusters should be as easy as moving a pod between nodes.

Take a multi-tenant control plane for everything, and add to it most of the world's cloud-native workloads happily cruising across clouds, datacenters, and out to the edge...

That's **`kcp`**.


## Can I start using `kcp` today?

Yes! This work is still in early development, which means it's _not ready for production_ and _APIs, commands, flags, etc. are subject to change_, but also that your feedback can have a big impact. Please try it out and [let us know](#this-sounds-cool-and-i-want-to-help) what you like, dislike, what works, what doesn't, etc.

`kcp` is currently a prototype, not a project. We're exploring these ideas here to try them out, experiment, and bounce them off each other.

## Latest demo: v0.4
Check out our latest demo recording showing workspaces, transparent multi-cluster, and API inheritance!

[![Watch the video](https://img.youtube.com/vi/0trUxoQqdNk/0.jpg)](https://youtu.be/0trUxoQqdNk)

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

First of all, be sure to have [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl) and [Go](https://golang.org/doc/install) (1.17+) installed.

After cloning the repository, you can build `kcp` and our `kubectl kcp` plugins using this command:

```
make install
```

Ensure that your `${PATH}` contains the output directory of `go install`, and start `kcp` on your machine with:

```
kcp start
```

This will build and run your kcp server, and generate a kubeconfig in `.kcp/admin.kubeconfig` you can use to connect to it:

```
export KUBECONFIG=.kcp/admin.kubeconfig
kubectl api-resources
```

Check out our [Contributing](CONTRIBUTING.md) and [Developer](docs/developers) guides.

## Workspaces

Each workspace delivers to its users and clients the behavior of a
distinct Kubernetes API service --- that is, the service provided by
the kube-apiservers of a Kubernetes cluster.  Each workspace has its
own namespaces and CRDs, for example.  Workspaces are implemented by
logical clusters and are the user interface to them.  It is intended
that users, and clients that need such awareness, deal with workspaces
rather than directly with logical clusters.

The workspaces are arranged in a tree, like directories in a
filesystem.  The root is known as `root`.  Pathnames use colon (`:`)
as the separator, rather than the forward or backward slash commonly
found in filesystems.  An absolute pathname is one that starts with
`root`.  For example: when I started kcp and went to my home
directory, I found its absolute pathname to be
`root:users:zu:yc:kcp-admin`.

There are a few different types of workspace built into the kcp
binary, and developers can add more workspace types by creating API
objects of type `ClusterWorkspaceType` in the root workspace.  You can
see the list of available types by doing `kubectl get
ClusterWorkspaceType` in the root workspace.

Only certain pairings of parent and child workspace type are allowed.
Each type of workspace may limit the types of parents and/or the
types of children that are allowed.

Following is a description of the path to my home workspace.

| pathname | type |
| -------- | ---- |
| root | root |
| root:users | homeroot |
| root:users:zu | homebucket |
| root:users:zu:yc | homebucket |
| root:users:zu:yc:kcp-admin | home |

The ordinary type of workspace for regular users to use is
`universal`.  It allows any type of parent, and both `home`
and `universal` allow `universal` children.

There are also types named `organization` (whose parent must be
`root`) and `team` (whose parent must be an `organization`).

The CLI to workspaces is `kubectl kcp workspace`.  You can abbreviate
`workspace` as `ws`.  You can skip the `kcp` part, because `make
install` also installs `kubectl` plugins named `ws` and `workspace`.
Thus, `kubectl ws` is the same as `kubectl kcp workspace`.

This CLI offers navigation similar to Unix/Linux `cd`, state query
similar to `pwd`, and creation similar to `mkdir`.  Use `kubectl kcp
ws --help` to get details.  You can also use `kubectl get workspaces` to list
children and `kubectl delete workspace` to `rmdir`.

## Using VSCode

A pre-configured VSCode workspace is available in `contrib/kcp.code-workspace`. You can use the `Launch kcp` configuration
to start the KCP lightweight API Server in debug mode inside VSCode.

## This sounds cool and I want to help!

Thanks! And great! Please check out:

* [Good first issue](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22) issues
* [Help wanted](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22help+wanted%22) issues

You can also reach us here, in this repository via [issues](https://github.com/kcp-dev/kcp/issues) and [discussions](https://github.com/kcp-dev/kcp/discussions), or:

- Join the [`#kcp-dev` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io)
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
