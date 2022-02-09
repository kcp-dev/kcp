# KCP Ingress

PoC related to <https://github.com/kcp-dev/kcp/issues/75>

## Getting Started

Clone the KCP repo and run:

```bash
make demo-ingress 
```

This script will:

- build all the binaries
- deploy two k8s (Kind) clusters locally.
- deploy and configure the ingress controllers in each cluster.
- start the KCP server.

Then you will be able to follow an interactive demo of the ingress controller.

## Envoy control plane

ingress-controller contains a small Envoy control-plane for local development purposes. It reads an Ingress V1 resources and creates the Envoy configuration. It is not intended to be used in production, and doesn't cover all the features of Ingress v1.

To enable it, run:

```bash
./bin/ingress-controller -kubeconfig .kcp/admin.kubeconfig -envoyxds
```

Then you can run the Envoy server using the bootstrap config provided:

```bash
envoy -c contrib/envoy/bootstrap.yaml
```

By default, the Envoy server will listen on port 80, and that can be controlled with the `-envoy-listener-port` flag. 

## Overall diagram

```
                                                                                                   XDS API
                                                 ┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
                        ┌────────────────────────┼────────────────────────────────┐                                                                                 │
                        │                        ▼                                │                                                                                 │
                        │ KCP       ┌────────────────────────┐                    │                                                                                 │
                        │           │                        │                    │                      ┌───────────────────────────────┐                          │
                        │           │ KCP-Ingress Controller │─────Creates─┐      │                      │                               │                          │
                        │           │                        │             │      │                      │            ┌────────────────┐ │                          │
                        │           └────────────────────────┘             │      │                      │          ┌▶│  Leaf Ingress  │ │                          │
                        │                        │                         ▼      │  Sync Object and status         │ └────────────────┘ │                          │
                        │                        │            ┌───────────────────┴────┐                 │          │                 ┌──┴───────┐                  │
                        │                        ▼            │                        │           ┌─────┴────────┐ │                 │          │                  │
        ┌──────────┐    │           ┌────────────────────────┐│      Leaf Ingress      │◀─────────▶│    Syncer    │─┘8s cluster┌─────▶│ Gateway  │◀──┐              │
        │Ingress   │    │           │                        ││                        │           └─────┬────────┘            │      │          │   │              │
        │HTTPRoute │────┼──────────▶│      Root Ingress      │├────────────────────────┤                 │                     │      └──┬───────┘   │              │
        │Route     │    │           │                        ││                        │                 │                     │         │           │              │
        └──────────┘    │           └────────────────────────┘│      Leaf Ingress      │◀───────────┐    │       ┌───────────────────────┴──┐        │              │
                        │                        ▲            │                        │            │    │       │                          │        │              │
                        │                        │            ├────────────────────────┤            │    │       │  gateway-api controller  │        │              │
                        │                        │            │                        │            │    └───────┤                          │        │              │
                        │                        │            │      Leaf Ingress      │◀───────┐   │            └──────────────────────────┘        │              │
                        │                        │            │                        │        │   │    ┌────────────────────────────────┐          │              │
                        │                        │            └───────────────────┬────┘        │   │    │              ┌────────────────┐│          │              │
                        │                        │                         │      │             │   │    │            ┌▶│  Leaf Ingress  ││          │              │
                        │                        │                         │      │             │   │    │            │ └────────────────┘│          │              │
                        │                        └─────────────────────────┘      │             │   │┌───┴──────────┐ │                   │          │              │                  ┌─────────────────┐
                        │                                  Merge Status           │             │   ▼│    Syncer    │─┘               ┌───┴──────┐   │   ┌──────────────────┐          │                 │
                        │                                                         │             │    └───┬──────────┘                 │          │   │   │                  │          │                 │
                        │                                                         │             │        │          k8s cluster ┌────▶│ Gateway  │◀──┼───│      Envoy       │◀─────────│    Requests     │
                        │                                                         │             │        │                      │     │          │   │   │                  │          │                 │
                        │                                                         │             │        │                      │     └───┬──────┘   │   └──────────────────┘          │                 │
                        │                                                         │             │        │                      │         │          │                                 └─────────────────┘
                        │                                                         │             │        │                      │         │          │
                        │                                                         │             │        │        ┌───────────────────────┴──┐       │
                        │                                                         │             │        │        │                          │       │
                        │                                                         │             │        └────────┤  gateway-api controller  │       │
                        │                                                         │             │                 │                          │       │
                        │                                                         │             │                 └──────────────────────────┘       │
                        └─────────────────────────────────────────────────────────┘             │        ┌───────────────────────────────┐           │
                                                                                                │        │             ┌────────────────┐│           │
                                                                                                │        │           ┌▶│  Leaf Ingress  ││           │
                                                                                                │   ┌────┴─────────┐ │ └────────────────┘│           │
                                                                                                └───┤    Syncer    │─┘                   │           │
                                                                                                    └────┬─────────┘                  ┌──┴───────┐   │
                                                                                                         │                            │          │   │
                                                                                                         │          k8s cluster  ┌───▶│ Gateway  │◀──┘
                                                                                                         │                       │    │          │
                                                                                                         │                       │    └──┬───────┘
                                                                                                         │                       │       │
                                                                                                         │                       │       │
                                                                                                         │         ┌─────────────────────┴────┐
                                                                                                         │         │                          │
                                                                                                         └─────────┤  gateway-api controller  │
                                                                                                                   │                          │
                                                                                                                   └──────────────────────────┘
```