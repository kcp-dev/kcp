---
description: >
  Set up the Model Context Protocol (MCP) server to enable AI assistants to interact with kcp.
---

# MCP Server Integration

The [Kubernetes MCP Server](https://github.com/containers/kubernetes-mcp-server) provides a Model Context Protocol interface that enables AI assistants (like Claude, ChatGPT, etc.) to interact with Kubernetes clusters. When configured for kcp, it allows AI assistants to manage workspaces, logical clusters, and other kcp resources.

## Overview

The MCP server acts as a bridge between AI assistants and kcp, translating natural language requests into kcp API operations. It supports:

- Workspace management (create, list, navigate)
- Logical cluster operations
- Resource management within workspaces
- kcp-specific toolsets

## Authentication Modes

The MCP server supports two authentication modes:

| Mode | Description |
|------|-------------|
| **OIDC/Bearer Token** (recommended) | Validates JWT tokens from an OIDC provider and uses them for kcp API calls. Each user authenticates with their own identity. |
| **Kubeconfig** | Uses static credentials from a kubeconfig file. All requests use the same identity. |

This guide focuses on OIDC authentication, which allows the MCP server to use the same identity provider as your kcp deployment.

## How OIDC Authentication Works

When running with OIDC enabled:

1. The MCP server receives an `Authorization: Bearer <jwt-token>` header with each request
2. The server validates the JWT against the OIDC provider
3. A Kubernetes client is created using that bearer token
4. All kcp API calls are made with the user's token, preserving their identity and permissions

## Prerequisites

- A running kcp deployment with OIDC authentication (see [Helm installation](helm.md) or [production deployments](production/index.md))
- Helm 3.x installed
- Access to create Secrets and ConfigMaps in your cluster
- The same OIDC provider configured for both kcp and the MCP server

## Installation

### 1. Create the Base Kubeconfig Secret

The MCP server needs a base kubeconfig that provides the kcp API server endpoint and TLS settings. This kubeconfig does **not** need authentication credentials - the bearer token from OIDC replaces them.

The kcp-operator can generate this kubeconfig automatically using the `Kubeconfig` custom resource:

```bash
kubectl apply -f - <<EOF
apiVersion: operator.kcp.io/v1alpha1
kind: Kubeconfig
metadata:
  name: kcp-mcp-kubeconfig
  namespace: kcp-mcp
spec:
  # No username/groups needed - OIDC provides identity
  validity: 8766h # 1 year
  secretRef:
    name: kcp-mcp-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
      namespace: <kcp-namespace>  # namespace where your FrontProxy is deployed
EOF
```

The operator will create a Secret named `kcp-mcp-kubeconfig` containing the kubeconfig with the front proxy endpoint and CA certificate.

Alternatively, create a minimal kubeconfig manually:

```bash
kubectl create namespace kcp-mcp
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: kcp-mcp-kubeconfig
  namespace: kcp-mcp
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        certificate-authority-data: <CA_DATA>
        server: https://api.your-kcp.example.com:6443
      name: kcp
    contexts:
    - context:
        cluster: kcp
      name: kcp
    current-context: kcp
EOF
```

### 2. Create the Configuration ConfigMap

Create a ConfigMap with the MCP server configuration:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: kcp-mcp-config
  namespace: kcp-mcp
data:
  config.toml: |
    # Cluster provider strategy - use "kcp" for kcp control planes
    cluster_provider_strategy = "kcp"

    # Base kubeconfig path - provides cluster endpoint and TLS settings
    kubeconfig = "/etc/kubernetes-mcp-server/kubeconfig"

    # OIDC Authentication Configuration
    require_oauth = true
    authorization_url = "https://auth.example.com"
    oauth_audience = "your-client-id"

    # Optional: CA certificate for OIDC provider TLS verification
    # certificate_authority = "/etc/kubernetes-mcp-server/oidc-ca.crt"

    # Toolsets to enable - kcp and core provide kcp-specific functionality
    toolsets = ["kcp", "core"]

    # Disable pod and node related tools as they are not applicable to kcp
    disabled_tools = [
        "pods_list",
        "pods_list_in_namespace",
        "pods_get",
        "pods_delete",
        "pods_top",
        "pods_exec",
        "pods_log",
        "pods_run",
        "nodes_log",
        "nodes_stats_summary",
        "nodes_top",
    ]
EOF
```

### 3. Install the Helm Chart

Install the Kubernetes MCP Server using the official Helm chart:

```bash
helm upgrade -i kubernetes-mcp-server \
  oci://ghcr.io/containers/charts/kubernetes-mcp-server \
  -n kcp-mcp \
  --set ingress.host=mcp.your-kcp.example.com \
  -f values.yaml
```

Example `values.yaml` for kcp with OIDC:

```yaml
replicaCount: 1

image:
  registry: quay.io
  repository: containers/kubernetes_mcp_server
  version: latest
  pullPolicy: IfNotPresent

service:
  type: ClusterIP
  port: 8080

ingress:
  enabled: true
  host: "mcp.your-kcp.example.com"
  path: /
  pathType: ImplementationSpecific
  termination: edge

serviceAccount:
  create: true

# Disable default RBAC - kcp manages authorization
rbac:
  create: false

# Base kubeconfig provides cluster endpoint/TLS only
# Authentication is handled via OIDC bearer tokens
extraVolumes:
  - name: kubeconfig
    secret:
      secretName: kcp-mcp-kubeconfig
  - name: config
    configMap:
      name: kcp-mcp-config

extraVolumeMounts:
  - name: kubeconfig
    mountPath: /etc/kubernetes-mcp-server/kubeconfig
    subPath: kubeconfig
    readOnly: true
  - name: config
    mountPath: /etc/kubernetes-mcp-server/config.toml
    subPath: config.toml
    readOnly: true

configFilePath: /etc/kubernetes-mcp-server/config.toml

resources:
  limits:
    cpu: 100m
    memory: 128Mi
  requests:
    cpu: 100m
    memory: 128Mi
```

## Configuration Options

### OIDC Settings

| Setting | Description |
|---------|-------------|
| `require_oauth` | Set to `true` to enable OIDC authentication |
| `authorization_url` | The OIDC issuer URL (e.g., `https://auth.example.com`) |
| `oauth_audience` | The expected audience claim in the JWT (typically the client ID) |
| `certificate_authority` | Optional path to CA certificate for OIDC provider TLS |

### Cluster Provider Strategy

| Strategy | Description |
|----------|-------------|
| `kcp` | Enables kcp-specific functionality including workspace navigation and logical cluster management |
| `kubeconfig` | Standard Kubernetes mode (default) |

### Toolsets

Available toolsets for kcp:

| Toolset | Description |
|---------|-------------|
| `kcp` | kcp-specific tools for workspace and logical cluster management |
| `core` | Core Kubernetes resource management |

### Disabled Tools

For kcp deployments, disable tools that don't apply:

- **Pod tools**: kcp manages workspaces, not pods directly
- **Node tools**: kcp doesn't have nodes in the traditional sense

## Matching kcp OIDC Configuration

Ensure the MCP server OIDC settings match your kcp front-proxy configuration:

**kcp front-proxy OIDC settings:**

```yaml
auth:
  oidc:
    issuerURL: https://auth.example.com
    clientID: platform-mesh
    groupsClaim: groups
    usernameClaim: email
    usernamePrefix: "oidc:"
    groupsPrefix: "oidc:"
```

**Corresponding MCP server settings:**

```toml
require_oauth = true
authorization_url = "https://auth.example.com"
oauth_audience = "platform-mesh"
```

The `authorization_url` should match `issuerURL` and `oauth_audience` should match `clientID`.

## Verifying the Installation

1. Check the pod is running:

```bash
kubectl get pods -n kcp-mcp
```

2. Check the logs:

```bash
kubectl logs -n kcp-mcp -l app.kubernetes.io/name=kubernetes-mcp-server
```

3. Test the health endpoint:

```bash
curl https://mcp.your-kcp.example.com/healthz
```

4. Test with a bearer token:

```bash
# Get a token from your OIDC provider
TOKEN=$(kubectl oidc-login get-token --oidc-issuer-url=https://auth.example.com --oidc-client-id=platform-mesh | jq -r '.status.token')

# Test the MCP endpoint with the token
curl -H "Authorization: Bearer $TOKEN" https://mcp.your-kcp.example.com/api/v1/tools
```

## Connecting AI Assistants

Once the MCP server is running, configure your AI assistant to use the MCP endpoint with OIDC authentication:

**Endpoint:** `https://mcp.your-kcp.example.com`

**Authentication:** Bearer token from your OIDC provider

The AI assistant must obtain a valid JWT from the OIDC provider and include it in the `Authorization` header for all requests. The specific configuration depends on your AI assistant - refer to:

- [Kubernetes MCP Server documentation](https://github.com/containers/kubernetes-mcp-server)
- Your AI assistant's MCP configuration guide

## Troubleshooting

### Authentication Issues

If the MCP server rejects requests:

1. Verify the bearer token is valid and not expired
2. Check that `authorization_url` matches the kcp OIDC issuer
3. Ensure `oauth_audience` matches the kcp client ID
4. Check MCP server logs for JWT validation errors

### Connection Issues

If the MCP server cannot connect to kcp:

1. Verify the kcp API server URL in the base kubeconfig
2. Check network connectivity from the MCP pod to kcp
3. Verify TLS certificates are valid

### Authorization Issues

If requests are rejected by kcp:

1. Check that the user has appropriate RBAC permissions in kcp
2. Verify the JWT contains the expected claims (email, groups)
3. Ensure kcp's OIDC prefixes match the token claims

### Tool Errors

If specific tools return errors:

1. Verify the toolset configuration in `config.toml`
2. Check that disabled tools don't include tools you need
3. Review MCP server logs for detailed error messages

## Example Production Deployment

For production deployments matching the kcp-vespucci setup, see the example configuration in [contrib/production/kcp-mcp](https://github.com/kcp-dev/kcp/tree/main/contrib/production/kcp-mcp).
