# TILT

Tilt setup for kcp development.
The benefit of using Tilt here is that it can be used to build and deploy the kcp
automatically when code changes are detected. It also provides tools like
Prometheus, Grafana, Loki and port forwarding into local machines for debugging.
It uses a helm chart as a base and injects locally built images into kind cluster

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Tilt](https://docs.tilt.dev/install.html)
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [Helm](https://helm.sh/docs/intro/install/)
- [kubectl oidc-login](https://github.com/int128/kubelogin)

## Usage

To start tilt run:

```bash
./contrib/tilt/kind.sh
```
or
```bash
make tilt-kind-up
```

# Output example:
....
Install kcp
Tooling:
Grafana: http://localhost:3333/
Prometheus: http://localhost:9091
kcp API Server: https://localhost:9443
kcp FrontProxy Server: https://localhost:9444
Tilt started on http://localhost:10350/
v0.33.6, built 2023-09-29

(space) to open the browser
(s) to stream logs (--stream=true)
(t) to open legacy terminal mode (--legacy=true)
(ctrl-c) to exit
```

Once the tilt starts, press `space` and track the progress. The first boot might take
a while as it needs to build all the images, run Prometheus, Grafana, loki, etc.

### Shared etcd

All kcp components (the root shard, the theseus shard and their embedded cache
servers) share a single etcd instance deployed in the `kcp-etcd` namespace. Each
component is isolated by a distinct etcd key prefix rather than running its own
etcd:

- root shard: `--etcd-prefix=/shard/root`
- theseus shard: `--etcd-prefix=/shard/theseus`
- embedded cache servers: fixed `/cache` prefix (keys are shard-scoped)

### Disabling the observability stack

The observability stack (Grafana, Loki, Prometheus, Promtail) is deployed by
default. To skip it — e.g. on a resource-constrained machine or when you only need
kcp itself — set `KCP_OBSERVABILITY_ENABLED=false` before starting Tilt. This works
for both the default and the static install:

```bash
KCP_OBSERVABILITY_ENABLED=false make tilt-kind-up
```

(`0`, `no` and `off` are also accepted as disabling values.)

### Portal (contrib-dashboard)

Both installs deploy the [contrib-dashboard](https://github.com/kcp-dev/contrib-dashboard)
portal — a web UI for kcp workspaces and APIExports/APIBindings — served in no-oidc
mode against the front-proxy. Once up, it is reachable at <http://localhost:8080>.

Both read the portal's `deploy/deployment.yaml` from a sibling `contrib-dashboard`
checkout (default `../../../contrib-dashboard`, override with `KCP_PORTAL_DIR`).

- The default `Tiltfile` **live-reloads** the portal: it builds the image from that
  checkout and rebuilds on source changes.
- `Tiltfile.static` deploys the **published** image
  `ghcr.io/kcp-dev/contrib-dashboard:main` (no build). Override the ref with
  `KCP_PORTAL_IMAGE`.

Disable the portal in either install with `KCP_PORTAL_ENABLED=false` (also accepts
`0`, `no`, `off`):

```bash
KCP_PORTAL_ENABLED=false make tilt-kind-up
```

## Static install (upstream images, no hot reload)

The default `Tiltfile` builds the `kcp` and `kcp-front-proxy` images from your local
sources and hot-reloads on code changes — use it when developing kcp itself.

If you instead just want a working kcp to develop *against*, use `Tiltfile.static`.
It installs the upstream kcp-operator and lets it deploy the upstream,
version-matched kcp images. Nothing is built locally and code changes are not
reloaded.

```bash
./contrib/tilt/kind-static.sh
```
or
```bash
make tilt-kind-up-static
```

Both variants share the same cluster name and URLs, so run only one at a time. The
admin kubeconfigs are written to the repository root as `tilt-frontproxy.kubeconfig`,
`tilt-root.kubeconfig` and `tilt-theseus.kubeconfig`.

### Host resolution

The kcp hostnames must resolve to `127.0.0.1` so the Tilt port-forward (on
`:8443`) can be reached. The `.localhost` TLD is **not** auto-resolved on macOS,
so add this line to `/etc/hosts`:

```
127.0.0.1 kcp.localhost root.kcp.localhost theseus.kcp.localhost
```


# Login using IDP:

```bash
./contrib/tilt/generate-admin-kubeconfig.sh

export KUBECONFIG=kcp.kubeconfig

# create ws using kcp-admin
kubectl ws create test

# login using oidc
# user: admin@kcp.dev
# password: password
kubectl ws use ~ --user oidc
kubectl ws create test --user oidc
```

Check token manually if failed:
```bash
kubectl oidc-login get-token \
--oidc-issuer-url=https://idp.dev.local:6443 \
--oidc-client-id=kcp-dev \
--oidc-client-secret=Z2Fyc2lha2FsYmlzdmFuZGVuekWplCg== \
--insecure-skip-tls-verify=true \
--oidc-extra-scope=email
```

If you get `Unauthorized` error, check if you have cache contamination from previous runs:
```bash
rm -rf ~/.kube/cache/oidc-login
```
