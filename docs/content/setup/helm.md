---
description: >
  Install kcp on an existing Kubernetes cluster via the official Helm chart.
---

# Installation with Helm

## `kcp` Chart

We provide a [Helm](https://helm.sh/) chart to install kcp on top of an existing Kubernetes cluster.
Its source is available in [kcp-dev/helm-charts](https://github.com/kcp-dev/helm-charts).

The chart repository can be added via:

```sh
helm repo add kcp https://kcp-dev.github.io/helm-charts
```

The chart can then be installed as `kcp/kcp`:

```sh
helm install my-kcp kcp/kcp -f my-values.yaml
```

### Values

At the very least, `.externalHostname` needs to be set and point to a DNS hostname that will point
to the front-proxy's external hostname.

A list of all values is [available in the git repository](https://github.com/kcp-dev/helm-charts/blob/main/charts/kcp/values.yaml).

## Multi-Shard Charts

We are also working on a collection of charts that allow for a multi-[shard](../concepts/sharding/index.md) deployment.
Those are currently WIP and considered highly experimental, but instructions are also [available on GitHub](https://github.com/kcp-dev/helm-charts/tree/main/examples/sharded).
