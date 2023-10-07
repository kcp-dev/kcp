# Contributing to kcp

kcp is [Apache 2.0 licensed](https://github.com/kcp-dev/kcp/tree/main/LICENSE) and we accept contributions via
GitHub pull requests.

Please read the following guide if you're interested in contributing to kcp.

## Certificate of Origin

By contributing to this project you agree to the Developer Certificate of
Origin (DCO). This document was created by the Linux Kernel community and is a
simple statement that you, as a contributor, have the legal right to make the
contribution. See the [DCO](https://github.com/kcp-dev/kcp/tree/main/DCO) file for details.

## Getting started

### Prerequisites

1. Clone this repository.
2. [Install Go](https://golang.org/doc/install) (currently 1.20).
3. Install [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl).

Please note that the go language version numbers in these files must exactly agree: go/go.mod file, kcp/.ci-operator.yaml, kcp/Dockerfile, and in all the kcp/.github/workflows yaml files that specify go-version. In kcp/.ci-operator.yaml the go version is indicated by the "tag" attribute. In kcp/Dockerfile it is indicated by the "golang" attribute. In go.mod it is indicated by the "go" directive." In the .github/workflows yaml files it is indicated by "go-version"

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
- Configs intended for `NewForConfig` (i.e. today often called "admin workspace config") should uniformly be called `clusterConfig`
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
- When orgs land: `clusterName` or `fooClusterName` is always the fully qualified value that you can stick into obj.ObjectMeta.ClusterName. It's not necessarily the `(Cluster)Workspace.Name` from the object. For the latter, use `workspaceName` or `orgName`.
- Generally do `klog.Errorf` or `return err`, but not both together. If you need to make it clear where an error came from, you can wrap it.
- New features start under a feature-gate (`--feature-gate GateName=true`). (At some point in the future), new feature-gates are off by default *at least* until the APIs are promoted to beta (we are not there before we have reached MVP).
- Feature-gated code can be incomplete. Also their e2e coverage can be incomplete. **We do not compromise on unit tests**. Every feature-gated code needs full unit tests as every other code-path.
- Go Proverbs are good guidelines for style: https://go-proverbs.github.io/ – watch https://www.youtube.com/watch?v=PAAkCSZUG1c.
- We use Testify's [require](https://pkg.go.dev/github.com/stretchr/testify/require) a
  lot in tests, and avoid
  [assert](https://pkg.go.dev/github.com/stretchr/testify/assert).

  Note this subtle distinction of nested `require` statements:
  ```Golang
  require.Eventually(t, func() bool {
    foos, err := client.List(...)
    require.NoError(err) // fail fast, including failing require.Eventually immediately
    return someCondition(foos)
  }, ...)
  ```
  and
  ```Golang
  require.Eventually(t, func() bool {
    foos, err := client.List(...)
    if err != nil {
       return false // keep trying
    }
    return someCondition(foos)
  }, ...)
  ```
  The first fails fast on every client error. The second ignores client errors and keeps trying. Either
  has its place, depending on whether the client error is to be expected (e.g. because of asynchronicity making the resource available),
  or signals a real test problem.

### Using Kubebuilder CRD Validation Annotations

All of the built-in types for `kcp` are `CustomResourceDefinitions`, and we generate YAML spec for them from our Go types using [kubebuilder](https://github.com/kubernetes-sigs/kubebuilder).

When adding a field that requires validation, custom annotations are used to translate this logic into the generated OpenAPI spec. [This doc](https://book.kubebuilder.io/reference/markers/crd-validation.html) gives an overview of possible validations. These annotations map directly to concepts in the [OpenAPI Spec](https://swagger.io/specification/#data-type-format) so, for instance, the `format` of strings is defined there, not in kubebuilder. Furthermore, Kubernetes has forked the OpenAPI project [here](https://github.com/kubernetes/kube-openapi/tree/master/pkg/validation) and extends more formats in the extensions-apiserver [here](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1/types_jsonschema.go#L27).


### Replicated Data Types

Some objects are replicated and cached amongst shards when `kcp` is run in a sharded configuration. When writing code to list or get these objects, be sure to reference both shard-local and cache informers. To make this more convenient, wrap the look up in a function pointer.

For example:

```Golang

func NewController(ctx,
  localAPIExportInformer, cacheAPIExportInformer apisinformers.APIExportClusterInformer
) (*controller, error) {
  ...
  return &controller{
  listAPIExports: func(clusterName logicalcluster.Name) ([]apisv1apha1.APIExport, error) {
    exports, err := localAPIExportInformer.Cluster(clusterName).Lister().List(labels.Everything())
    if err != nil {
      return cacheAPIExportInformer.Cluster(clusterName).Lister().List(labels.Everything())
    }
    return exports, nil
  ...
  }
}
```

A full list of replicated resources is currently outlined in the [replication controller](https://github.com/kcp-dev/kcp/blob/main/pkg/reconciler/cache/replication/replication_controller.go).

### Getting your PR Merged

The `kcp` project uses `OWNERS` files to denote the collaborators who can assist you in getting your PR merged.  There
are two roles: reviewer and approver.  Merging a PR requires sign off from both a reviewer and an approver.


## Continuous Integration

kcp uses a combination of [GitHub Actions](https://help.github.com/en/actions/automating-your-workflow-with-github-actions) and
and [prow](https://github.com/kubernetes/test-infra/tree/master/prow) to automate the build process.

Here are the most important links:

- [.github/workflows/ci.yaml](https://github.com/kcp-dev/kcp/blob/main/.github/workflows/ci.yaml) defines the Github Actions based jobs.
- [kcp-dev/kcp/.prow.yaml](https://github.com/kcp-dev/kcp/blob/main/.prow.yaml) defines the prow based jobs.

## Debugging flakes

Tests that sometimes pass and sometimes fail are known as flakes. Sometimes, there is only an issue with the test, while
other times, there is an actual bug in the main code. Regardless of the root cause, it's important to try to eliminate
as many flakes as possible.

### Unit test flakes
If you're trying to debug a unit test flake, you can try to do something like this:

```shell
go test -race ./pkg/reconciler/apis/apibinding -run TestReconcileBinding -count 100 -failfast
```

This tests one specific package, running only a single test case by name, 100 times in a row. It fails as soon as it
encounters any failure. If this passes, it may still be possible there is a flake somewhere, so you may need to run
it a few times to be certain. If it fails, that's a great sign - you've been able to reproduce it locally. Now you
need to dig into the test condition that is failing. Work backwards from the condition and try to determine if the
condition is correct, and if it should be that way all the time. Look at the code under test and see if there are any
reasons things might not be deterministic.

### End to end test flakes

Debugging an end-to-end (e2e) test that is flaky can be a bit trickier than a unit test. Our e2e tests run in one of
two modes:

1. Tests share a single kcp server
2. Tests in a package share a single kcp server

The `e2e-shared-server` CI job uses mode 1, and the `e2e` CI job uses mode 2.

There are also a handful of tests that require a fully isolated kcp server, because they manipulate some configuration
aspects that are system-wide and would break all the other tests. These tests run in both `e2e` and `e2e-shared-server`,
separate from the other kcp instance(s).

You can use the same `run`, `-count`, and `-failfast` settings from the unit test section above for trying to reproduce
e2e flakes locally. Additionally, if you would like to operate in mode 1 (all tests share a single kcp server), you can
start a kcp instance locally in a separate terminal or tab:

```shell
bin/test-server
```

Then, to have your test use that shared kcp server, you add `-args --use-default-kcp-server` to your `go test` run:

```shell
go test ./test/e2e/apibinding -count 20 -failfast -args --use-default-kcp-server
```
## Community Roles

### Reviewers

Reviewers are responsible for reviewing code for correctness and adherence to standards. Oftentimes reviewers will
be able to advise on code efficiency and style as it relates to golang or project conventions as well as other considerations
that might not be obvious to the contributor.

### Approvers

Approvers are responsible for sign-off on the acceptance of the contribution. In essence, approval indicates that the
change is desired and good for the project, aligns with code, api, and system conventions, and appears to follow all required
process including adequate testing, documentation, follow ups, or notifications to other areas who might be interested
or affected by the change.

Approvers are also reviewers.

### Management of `OWNERS` files

If a reviewer or approver no longer wishes to be in their current role it is requested that a PR
be opened to update the `OWNERS` file. `OWNERS` files may be periodically reviewed and updated based on project activity
or feedback to ensure an acceptable contributor experience is maintained.
