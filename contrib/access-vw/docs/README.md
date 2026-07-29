# Access Virtual Workspace

Permission-aware workspace discovery for kcp. Answers "which workspaces does this
user have access to?" in a single API call — a **SelfClusterAccessReview** (SCAR) —
instead of N `SelfSubjectAccessReview`s.

The motivating consumer is MCP: an AI agent connected to a kcp installation needs a
workspace inventory scoped to the calling user, and no such endpoint exists today.
SCAR is generic though — CLIs, dashboards and other services can use it the same way.
See [kcp-dev/kcp#3839](https://github.com/kcp-dev/kcp/issues/3839).

## Why a separate module

This is its own Go module and its own binary, rather than another virtual workspace in
`cmd/virtual-workspaces`. It maintains an in-memory RBAC index built with
multicluster-runtime, and will later host an MCP protocol server — dependencies kcp
core does not otherwise carry. Keeping it out of the root module keeps those out of
kcp's dependency graph.

It builds against the in-repo staging modules (`sdk`, `virtual-workspace-framework`)
and the same kcp fork of Kubernetes as kcp core, via relative `replace` directives.
When bumping the root `go.mod`, keep this one in sync.

## How it works

1. **Indexing.** A provider watches `ClusterRoleBindings` and `RoleBindings` across
   every workspace that has bound the `access.kcp.io` APIExport, translating them into
   an in-memory permission graph of subjects (users, groups, service accounts) to
   logical clusters. Indexing is opt-in by design: a workspace is only discoverable
   once it binds the APIExport.

2. **Serving.** The binary runs a virtual-workspace root apiserver with the access VW
   registered at `/services/access`. `SelfClusterAccessReview` is a create-only REST
   resource, modelled on `SelfSubjectAccessReview`: the caller POSTs an empty object
   and gets it back with `status.clusters` filled in from the graph, for the identity
   the apiserver's authentication layer resolved.

3. **Consuming.** The response is a list of `(clusterName, endpoint)` pairs.
   `cmd/scar-to-kubeconfig` turns one into a scoped kubeconfig.

The `AccessProvider` interface is the seam: the RBAC provider ships here, and
deployments using an external authorizer (webhook, OpenFGA, other ReBAC systems)
implement the same interface without changing the SCAR API surface.

## Layout

| Path | Description |
|------|-------------|
| `cmd/access-vw` | Main binary: RBAC indexer + access VW. |
| `cmd/scar-to-kubeconfig` | Calls SCAR and writes a scoped kubeconfig. |
| `pkg/apis/access/v1alpha1` | `SelfClusterAccessReview` API types. |
| `pkg/graph` | In-memory permission graph. No kcp imports. |
| `pkg/accessprovider` | The `AccessProvider` seam. |
| `pkg/rbacprovider` | Watches CRBs/RBs, translates them into graph grants. |
| `pkg/server` | Options and wiring: serving, delegated authn, VW registration. |
| `pkg/virtual/scar` | SCAR REST storage and virtual workspace definition. |
| `config/apiexport` | `access.kcp.io` APIExport + APIResourceSchema. |
| `config/examples` | APIBinding for consumer workspaces to opt in. |

## Running

```sh
go build -o bin/access-vw ./cmd/access-vw

# Install the APIExport in root, then bind it from the workspaces you want indexed.
kubectl apply -f config/apiexport/

bin/access-vw \
  --kubeconfig=$KUBECONFIG \
  --apiexport-endpointslice=access.kcp.io \
  --endpoint-base=https://localhost:6443/clusters/ \
  --secure-port=9443
```

`--apiexport-endpointslice` names the `APIExportEndpointSlice` kcp generates for the
APIExport, so it matches the export's name (`access.kcp.io`).

Serving is TLS-only; a self-signed certificate is generated for development if none is
supplied. Callers are authenticated by the standard delegated stack — front-proxy
requestheader client certificates in production, bearer tokens via TokenReview when
reached directly.

Issue a review:

```sh
curl -k -XPOST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://localhost:9443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews
```

### Behind the front-proxy

Add to the front-proxy path-mapping file:

```yaml
- path: /services/access
  backend: https://access-vw.kcp-system.svc:9443
  backend_server_ca: /etc/kcp/tls/access-vw-ca.crt
  proxy_client_cert: /etc/kcp/tls/requestheader.crt
  proxy_client_key: /etc/kcp/tls/requestheader.key
```

and start the VW with `--requestheader-client-ca-file` pointing at the CA that signed
the front-proxy's requestheader client certificate.

## Status and known limits

Alpha. The SCAR API is `v1alpha1` and unstable.

The graph is eventually consistent: it is driven by watch events, so an access change
takes a moment to appear in SCAR results. Warrants and scopes are not yet modelled, so
in deployments that rely on them the answer can be wider than kcp's effective
authorization — the graph is derived from bindings alone. Treat SCAR as discovery, not
as an authorization decision: every subsequent call is still authorized by kcp.
