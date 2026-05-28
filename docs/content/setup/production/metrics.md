# Scraping kcp metrics

kcp exposes Prometheus metrics on the `/metrics` endpoint of every shard and of
the cache server. These are **shard-wide** resources: a single scrape returns
process-level data for the entire shard, not for any particular workspace.

This page describes how to authorize a scraper (e.g. Prometheus) without using
the `system:masters` group.

## Where `/metrics` lives

For each shard:

```
https://<shard>:6443/metrics
```

For the cache server:

```
https://<cache-server>:6443/metrics
```

Workspace- or shard-scoped variants such as

```
https://<shard>:6443/clusters/<workspace>/metrics
https://<cache-server>:6443/services/cache/shards/<shard>/clusters/<workspace>/metrics
```

return `501 Not Implemented`. Today this is a placeholder: per-workspace and
per-shard metrics are not yet implemented, and the data the underlying
`/metrics` handler exposes is shard-wide with no per-workspace or per-shard
meaning. The kcp HTTP filter rejects these requests before authorization runs
so that a future implementation can fill in real workspace-scoped metrics
without changing the URL contract.

## Authorizing a scraper on a shard

kcp ships a bootstrap `ClusterRole` named `system:kcp:metrics-reader` that grants
`GET` on `/metrics`. To allow an identity to scrape every shard, create a
`ClusterRoleBinding` in the `:root` workspace. The binding is replicated to all
shards via the cache server, so a single binding is enough.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: prometheus-metrics-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:kcp:metrics-reader
subjects:
  - kind: User
    apiGroup: rbac.authorization.k8s.io
    name: prometheus
```

Apply it against the root workspace:

```bash
kubectl --kubeconfig=admin.kubeconfig --context root ws use :root
kubectl apply -f prometheus-metrics-reader-binding.yaml
```

Any client authenticated as user `prometheus` (or whichever subject you bind)
can now scrape `/metrics` on every shard:

```bash
curl -k --cert prometheus.crt --key prometheus.key \
  https://<shard>:6443/metrics
```

Substitute `kind: Group` or `kind: ServiceAccount` in the binding to suit your
identity provider.

### Note: workspace-local `nonResourceURLs: /metrics` no longer works

Earlier kcp releases accidentally allowed a workspace administrator to grant
themselves access to shard metrics by creating a `ClusterRole` with
`nonResourceURLs: ["/metrics"]` and a binding inside their own workspace. This
was a privilege escalation: the data exposed is shard-wide, not workspace
content. The path is now rejected at the workspace scope and the only
authoritative binding is one created in `:root`.

## Authorizing a scraper on the cache server

The cache server has no workspace-aware RBAC. By default `/metrics` is reachable
only by `system:masters`. To allow unauthenticated scraping (typical for an
internal Prometheus running on the same private network), add `/metrics` to
the cache server's `--authorization-always-allow-paths` flag:

```
--authorization-always-allow-paths=/healthz,/readyz,/livez,/metrics
```

The same flag already governs liveness/readiness scraping, so this is the
established pattern. The cache server's request filter still rejects
shard/workspace-scoped variants of `/metrics` with `501 Not Implemented` even
when the path is added to the always-allow list.

## Prometheus scrape config example

```yaml
scrape_configs:
  - job_name: kcp-shards
    metrics_path: /metrics
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/kcp-ca.crt
      cert_file: /etc/prometheus/prometheus.crt
      key_file: /etc/prometheus/prometheus.key
    static_configs:
      - targets:
          - shard-1.kcp.svc:6443
          - shard-2.kcp.svc:6443
  - job_name: kcp-cache
    metrics_path: /metrics
    scheme: https
    tls_config:
      ca_file: /etc/prometheus/kcp-ca.crt
      insecure_skip_verify: true
    static_configs:
      - targets:
          - cache.kcp.svc:6443
```
