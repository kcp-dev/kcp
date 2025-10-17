> ⚠️ **This is an automatically published staged repository for kcp**.   
> Contributions, including issues and pull requests, should be made to the main kcp repository: [https://github.com/kcp-dev/kcp](https://github.com/kcp-dev/kcp).  
> This repository is read-only for importing, and not used for direct contributions.  
> See the [monorepo structure document](https://docs.kcp.io/kcp/main/contributing/monorepo/) for more details.

## Code Generators for KCP-aware clients, informers and listers

This repository contains code generation tools analogous to the Kubernetes
code-generator. It contains:

* `cluster-client-gen` to generate a cluster-aware clientset,
* `cluster-informer-gen` to generate cluster-aware informers and
* `cluster-lister-gen` to do the same for listers.

Note that you need to have generated the versioned Kubernetes clientset and
applyconfiguration packages already in order to generate and use cluster-aware
code. Single-cluster listers and informers however are optional and the
generator here can generate the necessary interfaces itself.

### Usage

It is strongly recommended to use the provided `cluster_codegen.sh`, which works
very much like Kubernetes' `kube_codegen.sh`. A common way to acquire it is to
have a synthetic Go dependency on `github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen`
(often done in a `hack/tools.go`) and then call it like so in your project:

```bash
# Often you would want to generate both the regular Kubernetes clientset and
# the cluster-aware clienset.

CODEGEN_PKG="$(go list -f '{{.Dir}}' -m k8s.io/code-generator)"
CLUSTER_CODEGEN_PKG="$(go list -f '{{.Dir}}' -m github.com/kcp-dev/code-generator/v3)"

source "$CODEGEN_PKG/kube_codegen.sh"
source "$CLUSTER_CODEGEN_PKG/cluster_codegen.sh"

# Now you can call kube::codegen:: and cluster::codegen:: functions.

kube::codegen::gen_client \
  --boilerplate hack/boilerplate/examples/boilerplate.generatego.txt \
  --output-dir pkg/generated \
  --output-pkg acme.corp/pkg/generated \
  --with-applyconfig \
  --applyconfig-name applyconfigurations \
  --with-watch \
  ./pkg/apis

cluster::codegen::gen_client \
  --boilerplate hack/boilerplate/examples/boilerplate.generatego.txt \
  --output-dir pkg/clients \
  --output-pkg acme.corp/pkg/clients \
  --with-watch \
  --single-cluster-versioned-clientset-pkg acme.corp/pkg/generated/clientset/versioned \
  --single-cluster-applyconfigurations-pkg acme.corp/pkg/generated/applyconfigurations \
  --single-cluster-listers-pkg acme.corp/pkg/generated/listers \
  --single-cluster-informers-pkg acme.corp/pkg/generated/informers/externalversions \
  pkg/apis
```

Please refer to the [cluster_codegen.sh](./cluster_codegen.sh) for more information
on the available command line flags.

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please check out [contributing to kcp](https://docs.kcp.io/kcp/main/contributing/).

This community has a [Code of Conduct](./code-of-conduct.md). Please make sure to follow it.

## Getting in touch

There are several ways to communicate with us:

- The [`#kcp-dev` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io).
- Our mailing lists:
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions.
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users.
- By joining the kcp-dev mailing list, you should receive an invite to our bi-weekly community meetings.
- See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q).
- The next community meeting dates are available via our [CNCF community group](https://community.cncf.io/kcp/).
- Check the [community meeting notes document](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4) for future and past meeting agendas.
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive.
