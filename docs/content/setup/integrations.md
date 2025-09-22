# Integrations

kcp integrates with several CNCF projects. This page documents known integrations. Please be aware that we try our best to keep it updated but rely on community contributions for that.

kcp has some "obvious" integrations e.g. with [Kubernetes](https://kubernetes.io) (since it can be deployed on a Kubernetes cluster) and [Helm](https://helm.sh) (since a Helm chart is maintained as the
primary installation method on Kubernetes).

The fact that kcp is compatible with the Kubernetes Resource Model (KRM) also means that projects using the Kubernetes API might be compatible. The [api-syncagent](https://docs.kcp.io/api-syncagent)
component also allows integration of *any* Kubernetes controller/operator in principle. An example of this can be found in our [KubeCon London workshop](https://docs.kcp.io/contrib/learning/20250401-kubecon-london/workshop/).

## multicluster-runtime

kcp integrates with [kubernetes-sigs/multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime) by providing a so-called provider which gives a controller dynamic
access to kcp workspaces. Multiple providers exists for different use cases, see [kcp-dev/multicluster-provider](https://github.com/kcp-dev/multicluster-provider) for a full overview.

## Dex

kcp integrates with any OIDC provider, which includes [Dex](https://dexidp.io). To use `kubectl` with it, [kubelogin](https://github.com/int128/kubelogin) is required.

To integrate them make sure to set up a static client in Dex that is configured similar to:

```yaml
staticClients:
- id: kcp-kubelogin
  name: kcp-kubelogin
  secret: <RANDOM-SECRET-HERE>
  RedirectURIs:
  - http://localhost:8000
  - http://localhost:18000
```

Which is then used by [kubelogin](https://github.com/int128/kubelogin) (warning: the secret is shared across all users!). Check its documentation for more details.

A kubeconfig's `users` configuration would look similar to this:

```yaml
users:
- name: oidc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      args:
      - oidc-login
      - get-token
      - --oidc-issuer-url=https://<url-to-dex>
      - --oidc-client-id=kcp-kubelogin
      - --oidc-client-secret=<RANDOM-SECRET-HERE>
      - --oidc-extra-scope=email,groups
      command: kubectl
      env: null
      interactiveMode: IfAvailable
      provideClusterInfo: false
```

## OpenFGA

kcp can integrate with [OpenFGA](https://openfga.dev/) via a shim webhook component that accepts kcp's [authorization webhooks](../concepts/authorization/authorizers.md#webhook-authorizer) and translates
them to OpenFGA queries.

!!! info "Third Party Solutions"
    A third-party example of such a webhook would be Platform Mesh's [rebac-authz-webhook](https://github.com/platform-mesh/rebac-authz-webhook).
