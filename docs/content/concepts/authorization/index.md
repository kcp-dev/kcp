---
description: >
  How to authorize requests to kcp.
---

# Authorization

Within workspaces, KCP implements the same RBAC-based authorization mechanism as Kubernetes.
Other authorization schemes (i.e. ABAC) are not supported.
Generally, the same (cluster) role and (cluster) role binding principles apply exactly as in Kubernetes.

In addition, additional RBAC semantics is implemented cross-workspaces, namely the following:

- **Workspace Content** access: the user needs `access` permissions to a workspace or be even admin.
- for some resources, additional permission checks are performed, not represented by local or Kubernetes standard RBAC rules; for example
    - workspace creation checks for organization membership (see above).
    - workspace creation checks for `use` verb on the `WorkspaceType`.
    - API binding via APIBinding objects requires verb `bind` access to the corresponding `APIExport`.
- **System Workspaces** access: system workspaces are prefixed with `system:` and are not accessible by users.

The details of the authorizer chain are documented in [Authorizers](./authorizers.md).

## Pages

{% include "partials/section-overview.html" %}
