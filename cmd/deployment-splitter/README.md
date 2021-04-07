# Deployment Splitter

The Deployment Splitter is responsible for watching `kcp` for [Deployment](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/) resources and creating corresponding child Deployments, one for each Cluster `kcp` knows about.

The underlying real clusters will react to the creation of these child Deployments by syncing them, creating Pods, and updating status, at which point the Deployment Splitter will react by aggregating that status back up to the root Deployment.

## Running

Run `kcp`

```
make
bin/kcp start
export KUBECONFIG=.kcp/data/admin.kubeconfig
kubectl config use-context admin
```

Run Cluster Controller

```
kubectl apply -f config/cluster.example.dev_clusters.yaml
bin/cluster-controller --kubeconfig=.kcp/data/admin.kubeconfig
```

Run Deployment Splitter

```
kubectl apply -f contrib/crds/apps/apps_deployments.yaml
bin/deployment-splitter --kubeconfig=.kcp/data/admin.kubeconfig
```

## TODO

Deployment Splitter is definitely _not_ a scheduler. It's not smart. We could make it smart?

- react to clusters being added/deleted and becoming unavailable by rebalancing replicas across children
- balance replicas across children based on advertised capabilities (which can change over time), observed load, etc.
- recreate deleted child deployments
- react to root deployments being scaled up/down

These features and more are already supported by other projects, such as [Karmada](https://github.com/karmada-io/karmada) and [kubefed](https://github.com/kubernetes-retired/federation).
