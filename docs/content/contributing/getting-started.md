# Getting Started

## Prerequisites

1. Clone the [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository.
2. [Install Go](https://golang.org/doc/install) (at least 1.23).
3. Install [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl).

Please note that the go language version numbers in these files must exactly agree: go/go.mod file, kcp/Dockerfile, and in all the kcp/.github/workflows yaml files that specify go-version. In kcp/Dockerfile it is indicated by the "golang" attribute. In go.mod it is indicated by the "go" directive." In the .github/workflows yaml files it is indicated by "go-version".

If you wish to use a newer Go version (with the risk that your changes might not successfully pass CI when submitted as pull request), you can set an environment variable to ignore the Go version requirement.

```sh
export IGNORE_GO_VERSION=1
```

## Developer Certificate of Origin (DCO)

Contributing to kcp requires a [Developer Certificate of Origin (DCO)](https://developercertificate.org/) on all commits so we can be sure that you are allowed to contribute the code in your pull request.

To accept the DCO, your commits need to be signed off. When creating a commit via `git`, you should append the `--signoff` / `-s` flag to the command, like this:

```sh
git commit -m "my commit message" --signoff
```

This will add a line to your commit that looks like this, stating that you are committing under the DCO:

```
Signed-off-by: Your Name <mail@example.com>
```

Please be aware that we cannot accept pull requests in which commits are missing the sign-off.


## Build & Verify

1. In one terminal, build and start `kcp`:

```sh
go run ./cmd/kcp start
```

2. In another terminal, tell `kubectl` where to find the kubeconfig:

```sh
export KUBECONFIG=.kcp/admin.kubeconfig
```

3. Confirm you can connect to `kcp`:

```sh
kubectl api-resources
```


## Finding Areas to Contribute

Starting to participate in a new project can sometimes be overwhelming, and you may not know where to begin. Fortunately, we are here to help! We track all of our tasks here in GitHub, and we label our issues to categorize them. Here are a couple of handy links to check out:

* [Good first issue](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22good+first+issue%22) issues
* [Help wanted](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3A%22help+wanted%22) issues

You're certainly not limited to only these kinds of issues, though! If you're comfortable, please feel free to try working on anything that is open.

We do use the assignee feature in GitHub for issues. If you find an unassigned issue, comment asking if you can be assigned, and ideally wait for a maintainer to respond. If you find an assigned issue and you want to work on it or help out, please reach out to the assignee first.

Sometimes you might get an amazing idea and start working on a huge amount of code. We love and encourage excitement like this, but we do ask that before you embarking on a giant pull request, please reach out to the community first for an initial discussion. You could [file an issue](https://github.com/kcp-dev/kcp/issues/new/choose), send a discussion to our [mailing list](https://groups.google.com/g/kcp-dev), and/or join one of our [community meetings](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4).

Finally, we welcome and value all types of contributions, beyond "just code"! Other types include triaging bugs, tracking down and fixing flaky tests, improving our documentation, helping answer community questions, proposing and reviewing designs, etc.
