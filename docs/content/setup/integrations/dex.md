# Dex

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
