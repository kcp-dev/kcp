# KCP Demos
This folder contains the scripts to several awesome KCP demos.

# Index

| Folder                                                           | Demo                                                                                                                                                                 |
|------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| [apiNegotiation-script](./apiNegotiation-script)                 | Shows how api resources are imported and negotiated between a logical and a physical cluster.                                                                        |
| [clusters](./clusters)                                           | Contains the templates and script to create clusters using `kind`                                                                                                    |
| [ingress-script](./ingress-script)                               | Demos ingress traffic to a deployment that is moved between clusters                                                                                                 |
| [kubecon-script](./kubecon-script)                               | Script of the demo at [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman (video)](https://www.youtube.com/watch?v=oaPBYUfdFE8) |
| [prototype2-script](./prototype2-script)                         | Demos features of the prototype2 like `WorkspaceShards`                                                                                                              |
| [sharding](./sharding)                                           | Demo of running multiple sharded `kcp` processes                                                                                                                             |
| [workspaceKubectlPlugin-script](./workspaceKubectlPlugin-script) | Demonstration of the `kubectl` plugin for kcp `workspaces`                                                                                                           |


# Running the demos

## Prerequisites
* [Install `kind`](https://github.com/kubernetes-sigs/kind)
* [Build KCP](https://github.com/kcp-dev/kcp#how-do-i-get-started)

## Running the demos
Simply run `./runDemoScripts.sh`
