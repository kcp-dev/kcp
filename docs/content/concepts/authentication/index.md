---
description: >
  How to authenticate requests to kcp.
---

# Authentication

KCP implements the same authentication mechanisms as Kubernetes, allowing the use of Kubernetes authentication strategies. The KCP server can also be configured to generate a local admin.kubeconfig file and a token hash file, enabling access to KCP as a shard admin. This authentication mechanism is then added to any existing Kubernetes authentication strategies from generic control plane settings.

## KCP server admin authentication

Admin Authenticator sets up user roles and groups and generates authentication tokens. The authentication process relies on Kubernetes authenticated group authenticator.

### Users and Groups

| **User Name**   | **Role**                                                                                                                           | **Groups**                           |
|-----------------|------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------|
| **shard-admin** | Member of the privileged system group. This user bypasses most KCP authorization checks.                                           | system:masters, system:authenticated |
| **kcp-admin**   | Member of the system:kcp:workspace:admin and system:kcp:workspace:access groups. This user is subject to KCP authorization checks. | system:kcp:workspace:admin           |
| **user**        | Regular non-admin user who is not a part of any predefined groups.                                                                 | None                                 |

### Generated kubeconfig contexts

KCP server generates a kubeconfig file (admin.kubeconfig) containing credentials for the predefined users. This file allows users to authenticate into different logical clusters.

| **Context Name** | **Cluster Endpoint** |
|------------------|----------------------|
| **root**         | /clusters/root       |
| **base**         | /clusters/base       |
| **system:admin** | /clusters/system:admin |
| **shard-base**   | /clusters/base       |



## KCP Front Proxy Authentication

The kcp-front-proxy is a reverse proxy that accepts client certificates and forwards Common Name and Organizations to backend API servers in HTTP headers. The proxy terminates TLS and communicates with API servers via mTLS. Traffic is routed based on paths.

There are enabled four authentication strategies in union.

* Client certificate
* Token file
* Service account
* OIDC

### Authentication flow with client certificate

``` mermaid
flowchart
	n1@{ label: "Rectangle" }
	n1["A client sends a request
  with a client certificate."] --- n2
	n2["The proxy verifies the
  certificate against a trusted CA."] --- n3
	n3["Filters specified
  groups from requests."] --- n4
	n4["Extracts the user and
  groups and passing them
  as HTTP access headers."] --- n5["Forwards the request
  to the KCP API server."]
```

### Groups filter

KCP Front Proxy drops or passes specific system groups before forwarding requests.
These can be passed by setting `--authentication-pass-on-groups` and `--authentication-drop-groups` flags. They accept a comma-separated list of group names.

By default, proxy is configured to drop `system:masters` and `system:kcp:logical-cluster-admin`.
This ensures that highly privileged users, do not receive elevated access when passing through the proxy.

## Pages

{% include "partials/section-overview.html" %}
