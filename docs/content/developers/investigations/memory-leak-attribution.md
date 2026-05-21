---
description: >
  Diagnosing memory and goroutine leaks in kcp, with pprof labels for
  attribution to a specific controller and logical cluster.
---

# Memory and goroutine leak attribution

This page documents how to diagnose memory and goroutine leaks in kcp, especially
ones that grow as logical clusters are created and deleted (see
[#4071](https://github.com/kcp-dev/kcp/issues/4071), [#3350](https://github.com/kcp-dev/kcp/issues/3350)).

It is aimed at maintainers debugging an instance that is gradually retaining
memory, and at contributors writing per-cluster controllers who want their
goroutines to be attributable in profiles.

## Background

Per-cluster controllers in kcp spawn goroutines whose lifetime is bounded by a
single logical cluster. When a cluster is deleted, the controller's context is
cancelled, but several classes of goroutine outlive that cancellation if they
were not started with a context-aware termination path. The dominant case is
informer `processorListener` goroutines registered via `AddEventHandler` on
shared informers: the listener is owned by the (long-lived) shared informer, so
nothing stops it when the (short-lived) per-cluster controller goes away.

Each leaked controller registration produces two goroutines (`pop` and `run`),
each with a buffered notification channel. Across thousands of cluster
create/delete cycles, retained memory grows without bound until the pod is
restarted.

## Enabling pprof

kcp inherits the upstream apiserver's `--profiling` flag, which is **on by
default**. There are two practical ways to query pprof.

### Secure port (default, requires auth)

Any running kcp serves pprof on the same secure port as the API:

```
https://<kcp-host>:<port>/debug/pprof/
https://<kcp-host>:<port>/debug/pprof/goroutine?debug=2
https://<kcp-host>:<port>/debug/pprof/heap
```

Use a kubeconfig that has access to the root admin endpoint, e.g. via
`kubectl proxy` or `kubectl get --raw /debug/pprof/goroutine?debug=2`.

### Unauth'd unix socket (recommended for debugging)

Start kcp with `--debug-socket-path`:

```
kcp start --debug-socket-path=/tmp/kcp-debug.sock ...
```

Then query the socket directly:

```
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=2' > goroutines.txt
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/heap' > heap.pprof
```

Quote the URL — in zsh, `?` is a glob and an unquoted URL with a query string
fails with `zsh: no matches found`.

The unix socket has no authn/authz, so do not enable this in production.

### In-cluster setups

For Tilt/Kind/local-up setups where kcp runs in a pod, port-forward the secure
port and use `kubectl get --raw`:

```
kubectl get --raw '/debug/pprof/goroutine?debug=2' > goroutines.txt
```

### With Prometheus + pprof together

`hack/run-with-prometheus.sh` brings up a local Prometheus on `localhost:9090`
and auto-configures it to scrape `localhost:6443/metrics` once kcp starts.
Combine it with `--debug-socket-path` to get both worlds — graph the leak in
Prometheus, drill into it with labeled pprof dumps:

```
./hack/run-with-prometheus.sh ./bin/kcp start --debug-socket-path=/tmp/kcp-debug.sock
```

Key series in Prometheus for leak hunting:

- `go_goroutines` — total live goroutine count. Should be flat across
  workspace churn; any upward slope is a goroutine leak.
- `go_memstats_heap_inuse_bytes` — committed heap. Climbs with retained
  objects (cached specs, listener buffers, per-cluster maps).
- `go_memstats_alloc_bytes` — alloc rate proxy.
- `process_resident_memory_bytes` — RSS, including non-Go memory.

Typical loop:

```
# Start Prometheus + kcp
./hack/run-with-prometheus.sh ./bin/kcp start --debug-socket-path=/tmp/kcp-debug.sock

# In another shell, drive workspace churn
kubectl-kcp quickstart --scenario workspaces --tree-depth 10 --tree-count 1000

# Watch http://localhost:9090/graph?g0.expr=go_goroutines and confirm slope
# When the count is clearly elevated, grab a labeled dump:
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=2' > after.txt
grep '# labels:' after.txt | sort | uniq -c | sort -rn
```

## Counting goroutines

The pprof goroutine endpoint comes in two formats:

- `debug=2` — one full stack trace per goroutine. Easy to read, large.
- `debug=1` — stacks aggregated by uniqueness, with a count prefix per stack.
  Much smaller; better for "where are all my goroutines coming from".

Total live goroutines, from a `debug=2` dump:

```
grep -c '^goroutine ' goroutines.txt
```

Total live goroutines, from a `debug=1` dump:

```
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=1' \
  | awk '/^[0-9]+ @/ {sum+=$1} END {print sum}'
```

Top 20 stack signatures by goroutine count:

```
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=1' \
  | awk '/^[0-9]+ @/ {n=$1; getline label; getline frame; print n"\t"frame}' \
  | sort -rn | head -20
```

## Reading goroutine labels

Per-cluster code paths are wrapped with [`pkg/pproflabels.Cluster`](https://github.com/kcp-dev/kcp/blob/main/pkg/pproflabels/labels.go),
which attaches `controller` and `logicalcluster` labels via `runtime/pprof.Do`.
Labels propagate to any goroutine spawned during the wrapped call, including
informer `processorListener` goroutines that get spawned when `AddEventHandler`
is invoked on an already-running shared informer.

Important: pprof emits labels in the **`debug=1` (aggregated)** format, not
`debug=2`. A labeled stack in `goroutine?debug=1` looks like:

```
29 @ 0x102437fb8 0x1023cc274 0x1023cbe44 ...
# labels: {"controller":"kcp-kube-quota", "logicalcluster":"2j5lsf4ydtnftsi3"}
#   github.com/kcp-dev/apimachinery/v2/third_party/informers.(*processorListener).run.func1+0x43
#   k8s.io/apimachinery/pkg/util/wait.BackoffUntil.func1+0x3f
...
```

`debug=2` outputs raw stacks one goroutine at a time and does not include
label metadata. Use it to inspect individual stacks, but use `debug=1` for
anything label-based.

The leading count (`29 @ ...`) is the number of goroutines that share this
stack. The `# labels:` line tells you which controller and which logical
cluster they belong to.

### Filtering by label

Work from the `debug=1` dump — that's where labels live.

To count goroutines per (controller, cluster) label:

```
awk '/^[0-9]+ @/ {n=$1; getline label} label ~ /^# labels:/ {c[label]+=n} END {for(k in c) printf "%6d %s\n", c[k], k}' goroutine.summary | sort -rn
```

To count goroutines per controller alone (collapse across clusters):

```
awk '/^[0-9]+ @/ {n=$1; getline label} label ~ /^# labels:/ {
  match(label, /"controller":"[^"]+"/); c[substr(label,RSTART,RLENGTH)]+=n
} END {for(k in c) printf "%6d %s\n", c[k], k}' goroutine.summary | sort -rn
```

To find every leaked stack owned by a specific cluster:

```
awk '/^[0-9]+ @/ {block=$0; next} /^#/ {block=block"\n"$0; next} /^$/ {if (block ~ /"logicalcluster":"2j5lsf4ydtnftsi3"/) print block; block=""}' goroutine.summary
```

To diff before/after a known-clean baseline (the technique used in
[#3350](https://github.com/kcp-dev/kcp/issues/3350)):

```
# Baseline: kcp running, no churn. Use debug=1 — labels only appear in the
# aggregated format.
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=1' > before.txt

# Run churn: create/delete N workspaces in a loop
kubectl-kcp quickstart --scenario workspaces --tree-depth 10 --tree-count 1000

# After churn
curl --unix-socket /tmp/kcp-debug.sock 'http://localhost/debug/pprof/goroutine?debug=1' > after.txt

# Compare counts per controller label
diff <(grep -oE '"controller":"[^"]+"' before.txt | sort | uniq -c) \
     <(grep -oE '"controller":"[^"]+"' after.txt  | sort | uniq -c)
```

Any controller whose count grew by ~N (or a multiple thereof) is leaking one or
more handler registrations per cluster.

## Heap profiles

`/debug/pprof/heap` returns a sampled heap profile. To attribute retained heap
to (controller, cluster) the program must also have labels set on the goroutine
that allocated the memory — `pprof.Do` does this automatically for everything
allocated during the wrapped call. View with:

```
go tool pprof -http=:8080 heap.pprof
```

The flame graph's "labels" facet exposes the `controller` and `logicalcluster`
labels.

## Wiring labels in new controllers

When introducing a per-cluster controller (anything bounded by a single logical
cluster's lifecycle), wrap the goroutine spawn with `pproflabels.Cluster`:

```go
import "github.com/kcp-dev/kcp/pkg/pproflabels"

go pproflabels.Cluster(ctx, ControllerName, clusterName, func(ctx context.Context) {
    // controller body: Run, Start, AddEventHandler, etc.
    // any goroutines spawned in here inherit the labels.
})
```

This is enough for both the controller's own work goroutines AND any informer
`processorListener` goroutines spawned by `AddEventHandler` while the shared
informer is already running. Both kinds of leaks become attributable.

If the wrapped code might call `AddEventHandler` *before* the shared informer
is started, the listener goroutines will be spawned later by
`sharedProcessor.run` — outside the labeled scope — and will not inherit
labels. In practice kcp starts shared informers at bootstrap and per-cluster
controllers register handlers afterwards, so this case is rare.

## Known leak sources

Cross-reference for current and historical leak fixes:

- [#3016](https://github.com/kcp-dev/kcp/issues/3016) — original memory/goroutine leak (closed)
- [#3350](https://github.com/kcp-dev/kcp/issues/3350) — workspace churn leaks ~61 goroutines per workspace; root cause is informer handler deregistration (closed; partial fix)
- [#3787](https://github.com/kcp-dev/kcp/issues/3787) — open epic: GC improvements (per-workspace footprint)
- [#4044](https://github.com/kcp-dev/kcp/pull/4044) — cluster-aware GC, merged 2026-04-21
- [#4071](https://github.com/kcp-dev/kcp/issues/4071) — open: deleting logical clusters does not free memory

## See also

- [`pkg/pproflabels`](https://github.com/kcp-dev/kcp/blob/main/pkg/pproflabels/labels.go) — the labeling helper
- [`test/integration/workspace/leak_test.go`](https://github.com/kcp-dev/kcp/blob/main/test/integration/workspace/leak_test.go) — `TestWorkspaceDeletionLeak`, the goroutine leak smoke test
- [Go runtime/pprof](https://pkg.go.dev/runtime/pprof) — `pprof.Do` documentation
