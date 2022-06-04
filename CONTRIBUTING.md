# Contributing to kcp

kcp is [Apache 2.0 licensed](LICENSE) and we accept contributions via
GitHub pull requests.

Please read the following guide if you're interested in contributing to kcp.

## Certificate of Origin

By contributing to this project you agree to the Developer Certificate of
Origin (DCO). This document was created by the Linux Kernel community and is a
simple statement that you, as a contributor, have the legal right to make the
contribution. See the [DCO](DCO) file for details.

## Getting started

### Prerequisites

1. Clone this repository.
2. [Install Go](https://golang.org/doc/install) (1.17+).
3. Install [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl).

### Build & verify
1. In one terminal, build and start `kcp`:
```
go run ./cmd/kcp start
```

2. In another terminal, tell `kubectl` where to find the kubeconfig:

```
export KUBECONFIG=.kcp/admin.kubeconfig
```

3. Confirm you can connect to `kcp`:

```
kubectl api-resources
```

## Finding areas to contribute

Starting to participate in a new project can sometimes be overwhelming, and you may not know where to begin. Fortunately, we are here to help! We track all of our tasks here in GitHub, and we label our issues to categorize them. Here are a couple of handy links to check out:

* [Good first issue](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22) issues
* [Help wanted](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22help+wanted%22) issues

You're certainly not limited to only these kinds of issues, though! If you're comfortable, please feel free to try working on anything that is open.

We do use the assignee feature in GitHub for issues. If you find an unassigned issue, comment asking if you can be assigned, and ideally wait for a maintainer to respond. If you find an assigned issue and you want to work on it or help out, please reach out to the assignee first.

Sometimes you might get an amazing idea and start working on a huge amount of code. We love and encourage excitement like this, but we do ask that before you embarking on a giant pull request, please reach out to the community first for an initial discussion. You could [file an issue](https://github.com/kcp-dev/kcp/issues/new/choose), send a discussion to our [mailing list](https://groups.google.com/g/kcp-dev), and/or join one of our [community meetings](https://github.com/kcp-dev/kcp/issues?q=is%3Aissue+is%3Aopen+label%3Acommunity-meeting).

Finally, we welcome and value all types of contributions, beyond "just code"! Other types include triaging bugs, tracking down and fixing flaky tests, improving our documentation, helping answer community questions, proposing and reviewing designs, etc.


## Priorities & milestones

We prioritize issues and features both synchronously (during community meetings) and asynchronously (Slack/GitHub conversations).

We group issues together into milestones. Each milestone represents a set of new features and bug fixes that we want users to try out. We aim for each milestone to take about a month from start to finish.

You can see the [current list of milestones](https://github.com/kcp-dev/kcp/milestones?direction=asc&sort=due_date&state=open) in GitHub.

For a given issue or pull request, its milestone may be:

- **unset/unassigned**: we haven't looked at this yet, or if we have, we aren't sure if we want to do it and it needs more community discussion
- **assigned to a named milestone**
- **assigned to `TBD`** - we have looked at this, decided that it is important and we eventually would like to do it, but we aren't sure exactly when

If you are confident about the target milestone for your issue or PR, please set it. If you don’t have permissions, please ask & we’ll set it for you.


## Epics

We use the [epic label](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3Aepic+) to track large features that typically involve multiple stories. When creating a new epic, please use the [epic issue template](https://github.com/kcp-dev/kcp/issues/new?assignees=&labels=epic&template=epic.md&title=).

Please make sure that you fill in all the sections of the template (it's ok if some of this is done later, after creating the issue). If you need help with anything, please let us know.

### Story tasks

Story tasks in an epic should generally represent an independent chunk of work that can be implemented. These don't necessarily need to be copied to standalone GitHub issues; it's ok if we just track the story in the epic as a task. On a case by case basis, if a story seems large enough that it warrants its own issue, we can discuss creating one.

Please tag yourself using your GitHub handle next to a story task you plan to work on. If you don't have permission to do this, please let us know by either commenting on the issue, or reaching out in Slack, and we'll assist you.

When you open a PR for a story task, please edit the epic description and add a link to the PR next to your task.

When the PR has been merged, please make sure the task is checked off in the epic.


## Tracking work

### Issue status and project board

We use the Github projects beta for project management, compare [our project board](https://github.com/orgs/kcp-dev/projects/1). Please add issues and PRs into the kcp project and update the status (new, in-progress, ...) for those you are actively working on.

### Unplanned/untracked work
If you find yourself working on something that is unplanned and/or untracked (i.e., not an open GitHub issue or story task in an epic), that's 100% ok, but we'd like to track this type of work too! Please file a new issue for it, and when you have a PR ready, mark the PR as fixing the issue.


## Coding guidelines & conventions

- Always be clear about what clients or client configs target. Never use an unqualified `client`. Instead, always qualify. For example:
    - `rootClient`
    - `orgClient`
    - `pclusterClient`
    - `rootKcpClient`
    - `orgKubeClient`
- Configs intended for `NewClusterForConfig` (i.e. today often called "admin workspace config") should uniformly be called `clusterConfig`
    - Note: with org workspaces, `kcp` will no longer default clients to the "root" ("admin") logical cluster
    - Note 2: sometimes we use clients for same purpose, but this can be harder to read
- Cluster-aware clients should follow similar naming conventions:
    - `crdClusterClient`
    - `kcpClusterClient`
    - `kubeClusterClient`
- `clusterName` is a kcp term. It is **NOT** a name of a physical cluster. If we mean the latter, use `pclusterName` or similar.
- In the syncer: upstream = kcp, downstream = pcluster. Depending on direction, "from" and "to" can have different meanings. `source` and `sink` are synonyms for upstream and downstream.
- Qualify "namespace"s in code that handle up- and downstream, e.g. `upstreamNamespace`, `downstreamNamespace`, and also `upstreamObj`, `downstreamObj`.
- Logging:
  - Use the `fmt.Sprintf("%s|%s/%s", clusterName, namespace, name` syntax.
  - Default log-level is 2.
  - Controllers should generally log (a) **one** line (not more) non-error progress per item with `klog.V(2)` (b) actions like create/update/delete via `klog.V(3)` and (c) skipped actions, i.e. what was not done for reasons via `klog.V(4)`.
- When orgs land: `clusterName` or `fooClusterNane` is always the fully qualified value that you can stick into obj.ObjectMeta.ClusterName. It's not necessarily the `(Cluster)Workspace.Name` from the object. For the latter, use `workspaceName` or `orgName`.
- Generally do `klog.Errorf` or `return err`, but not both together. If you need to make it clear where an error came from, you can wrap it.
- New features start under a feature-gate (`--feature-gate GateName=true`). (At some point in the future), new feature-gates are off by default *at least* until the APIs are promoted to beta (we are not there before we have reached MVP).
- Feature-gated code can be incomplete. Also their e2e coverage can be incomplete. **We do not compromise on unit tests**. Every feature-gated code needs full unit tests as every other code-path.
- Go Proverbs are good guidelines for style: https://go-proverbs.github.io/ – watch https://www.youtube.com/watch?v=PAAkCSZUG1c.

### Using Kubebuilder CRD Validation Annotations

All of the built-in types for `kcp` are `CustomResourceDefinitions`, and we generate YAML spec for them from our Go types using [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder).

When adding a field that requires validation, custom annotations are used to translate this logic into the generated OpenAPI spec. [This doc](https://book.kubebuilder.io/reference/markers/crd-validation.html) gives an overview of possible validations. These annotations map directly to concepts in the [OpenAPI Spec](https://swagger.io/specification/#data-type-format) so, for instance, the `format` of strings is defined there, not in kubebuilder. Furthermore, Kubernetes has forked the OpenAPI project [here](https://github.com/kubernetes/kube-openapi/tree/master/pkg/validation) and extends more formats in the extensions-apiserver [here](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1/types_jsonschema.go#L27).
