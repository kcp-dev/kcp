# Integrations

kcp integrates with several CNCF projects. This page documents known integrations. Please be aware that we try our best to keep it updated but rely on community contributions for that.

kcp has some "obvious" integrations e.g. with [Kubernetes](https://kubernetes.io) (since it can be deployed on a Kubernetes cluster) and [Helm](https://helm.sh) (since a Helm chart is maintained as the
primary installation method on Kubernetes).

The fact that kcp is compatible with the Kubernetes Resource Model (KRM) also means that projects using the Kubernetes API might be compatible. The [api-syncagent](https://docs.kcp.io/api-syncagent)
component also allows integration of *any* Kubernetes controller/operator in principle. An example of this can be found in our [KubeCon London workshop](https://docs.kcp.io/contrib/learning/20250401-kubecon-london/workshop/).

## Available Integrations

| Integration | Description |
|-------------|-------------|
| [MCP Server](mcp.md) | Model Context Protocol server for AI assistant integration |
| [Keycloak](keycloak.md) | OIDC provider for authentication |
| [Dex](dex.md) | Lightweight OIDC provider |
| [multicluster-runtime](multicluster-runtime.md) | Kubernetes-sigs multicluster-runtime provider |
| [OpenFGA](openfga.md) | Authorization via webhook shim |
| [Lima](lima.md) | Development VM for portable kcp testing |
