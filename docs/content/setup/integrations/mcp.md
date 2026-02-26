---
description: >
  Set up the Model Context Protocol (MCP) server to enable AI assistants to interact with kcp.
---

# MCP Server Integration

The [Kubernetes MCP Server](https://github.com/containers/kubernetes-mcp-server) provides a Model Context Protocol interface that enables AI assistants (like Claude, ChatGPT, etc.) to interact with Kubernetes clusters. When configured for kcp, it allows AI assistants to manage workspaces, logical clusters, and other kcp resources.

!!! warning "Known Limitation"
    The current MCP server integration does not have a clear cluster-inventory or "what clusters do I have access to" capability. We are actively working on better integration to address this limitation.

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
- **OIDC Provider with Dynamic Client Registration (DCR) support** - The MCP server uses DCR for OAuth flows. Your IdP must:
  - Support OpenID Connect Dynamic Client Registration
  - Have the MCP server hostname configured as a trusted host (see [Keycloak configuration](#keycloak-dcr-configuration) below)
  - Have clients configured with `kcp` as an allowed audience

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
  namespace: kcp-vespucci
spec:
  username: fake-user-for-mcp
  groups:
    - kcp:mcp-users
  validity: 8766h # 1 year
  secretRef:
    name: kcp-mcp-kubeconfig
  target:
    frontProxyRef:
      name: frontproxy
EOF
```

The operator will create a Secret named `kcp-mcp-kubeconfig` in the `kcp-vespucci` namespace. Copy it to the `kcp-mcp` namespace:

```bash
kubectl create namespace kcp-mcp
kubectl get secret kcp-mcp-kubeconfig -n kcp-vespucci -o json | \
  jq 'del(.metadata.namespace, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences)' | \
  kubectl apply -n kcp-mcp -f -
```

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

### 3. Create the TLS Certificate

Create a cert-manager Certificate to generate TLS certificates signed by Let's Encrypt:

```bash
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: mcp-tls
  namespace: kcp-mcp
spec:
  secretName: mcp-tls
  duration: 2160h # 90 days
  renewBefore: 360h # 15 days
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
    group: cert-manager.io
  dnsNames:
    - mcp.vespucci.example.com
EOF
```

The certificate will be stored in the `mcp-tls` secret and automatically renewed by cert-manager.

### 4. Install the Helm Chart

Install the Kubernetes MCP Server using the official Helm chart:

```bash
helm upgrade -i kubernetes-mcp-server \
  oci://ghcr.io/containers/charts/kubernetes-mcp-server \
  -n kcp-mcp \
  -f contrib/production/kcp-mcp/values.yaml
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
# Note: "config" name is reserved by the chart, so we use "kcp-config"
extraVolumes:
  - name: kubeconfig
    secret:
      secretName: kcp-mcp-kubeconfig
  - name: kcp-config
    configMap:
      name: kcp-mcp-config

extraVolumeMounts:
  - name: kubeconfig
    mountPath: /etc/kubernetes-mcp-server/kubeconfig
    subPath: kubeconfig
    readOnly: true
  - name: kcp-config
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
TOKEN=$(kubectl oidc-login get-token \
  --oidc-issuer-url=https://auth.keycloak.example.com/realms/kcp \
  --oidc-client-id=kcp \
  --oidc-extra-scope="email" \
  --oidc-extra-scope="groups" | jq -r '.status.token')

# Test the MCP endpoint with the token
curl -H "Authorization: Bearer $TOKEN" https://mcp.example.com/healthz
```

To do more extensive testing:

```bash
claude mcp add kcp-mcp https://mcp.example.com:8443/mcp \
  --transport http
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

## Keycloak DCR Configuration

The MCP server requires an OIDC provider that supports Dynamic Client Registration (DCR). When using Keycloak, you need to configure:

### 1. Enable Client Registration

In Keycloak Admin Console:

1. Navigate to **Realm Settings** → **Client Registration**
2. Go to the **Client Registration Policies** tab
3. Under **Trusted Hosts** policy, add your MCP server hostname (e.g., `mcp.vespucci.genericcontrolplane.io`)

### 2. Configure Client Audience

Each client that will authenticate to the MCP server needs `kcp` as an allowed audience:

1. Navigate to **Clients** → Select your client (e.g., `kcp`)
2. Go to **Client Scopes** tab
3. Add a dedicated scope or use the default scope
4. In the scope's **Mappers** tab, create a new mapper:
   - **Mapper type**: Audience
   - **Name**: `kcp-audience`
   - **Included Client Audience**: `kcp`
   - **Add to ID token**: On
   - **Add to access token**: On

## FAQ and Troubleshooting

### Policy 'Trusted Hosts' rejected request

**Error:** `Policy 'Trusted Hosts' rejected request to client-registration service. Details: URI doesn't match any trusted host or trusted domain`

**Cause:** The MCP server hostname is not configured as a trusted host in Keycloak's Client Registration Policies.

**Solution:** In Keycloak Admin Console:
1. Go to **Realm Settings** → **Client Registration** → **Client Registration Policies**
2. Edit the **Trusted Hosts** policy
3. Add your MCP server hostname (e.g., `mcp.vespucci.example.com`)

### Debugging MCP Connection Issues

Use the `--debug` flag with Claude CLI to see detailed connection information:

```sh
claude --debug
```

This will show:
- OAuth/OIDC flow details
- HTTP request/response headers
- Token validation errors
- Connection timeouts

### Invalid Audience Error

**Error:** Token rejected due to invalid audience claim.

**Cause:** The JWT token doesn't include `kcp` in the audience (`aud`) claim.

**Solution:** Configure your Keycloak client to include `kcp` as an audience (see [Configure Client Audience](#2-configure-client-audience) above).

## Example Production Deployment

For production deployments matching the kcp-vespucci setup, see the example configuration in [contrib/production/kcp-mcp](https://github.com/kcp-dev/kcp/tree/main/contrib/production/kcp-mcp).
