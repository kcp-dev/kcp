# Writing a Virtual Workspace

Virtual workspaces are API endpoints that serve a computed view of resources across logical
clusters. For example, the APIExport virtual workspace lets an API provider manage objects
in all workspaces which bound their export.

Virtual workspaces are not limited to the implementations shipped with kcp: the framework they
are built on is a standalone Go module,
[`github.com/kcp-dev/virtual-workspace-framework`](https://github.com/kcp-dev/virtual-workspace-framework),
and platform builders are encouraged to write their own virtual workspaces and run them next to kcp.

Due to their high privileges, virtual workspaces require special care during development.
The following document illustrates how authorization is wired for virtual workspaces and provides a
checklist with best practices for developers.

## How Authorization Is Wired

Authorization follows the Kubernetes delegated authorizer model. Using [`framework.VirtualWorkspace`](https://github.com/kcp-dev/virtual-workspace-framework/blob/main/framework/types.go), you embed an `authorizer.Authorizer`
in your virtual workspace.
At request time, your `Authorize` method is called: anything that does not return `DecisionAllow`
will result in the request being refused. For further details on possible authorization return values, please
consult the [Kubernetes authorizer package documentation](https://pkg.go.dev/k8s.io/apiserver/pkg/authorization/authorizer).

## Checklist

### Authorization

- [ ] **Fail closed.** Return `DecisionNoOpinion` or `DecisionDeny` for anything the
      authorizer does not positively recognize - unknown resources, missing context values,
      lookup errors.
- [ ] **Treat every URL path segment as untrusted input.** Logical cluster names, API domain
      keys, export names etc. come from the client. Verify that the referenced objects exist
      and that the relationship the view is built on actually holds in the target workspace.
- [ ] **Gate the endpoint on explicit RBAC in a real workspace.** Use
      [`delegated.NewDelegatedAuthorizer`](https://github.com/kcp-dev/kcp/blob/main/pkg/authorization/delegated/authorizer.go)
      to evaluate a SubjectAccessReview against the workspace that owns the view. Prefer a
      dedicated sub-resource as the permission (e.g. the APIExport virtual workspace requires the
      `apiexports/content` sub-resource in the provider's workspace). Do not invent an
      out-of-band permission model: access to your view should be grantable, auditable, and
      revocable with the same RBAC objects as everything else in kcp.
- [ ] **Enforce resource scope.** Refuse resources the view is not supposed to serve. For
      consent-based access (permission claims), only serve what the consumer accepted, with
      the accepted verb intersection.
- [ ] **Respect upper bounds that apply on the standard request path.** A virtual workspace
      must not become a bypass for controls like [maximal permission policy](../concepts/apis/exporting-apis.md#maximal-permission-policy).
      Chain the corresponding authorizer where the view serves bound resources
      (see `newAuthorizer` in kcp's `pkg/virtual/apiexport/builder/build.go` for the
      canonical chain).
- [ ] **Make decisions auditable.** Wrap each authorizer with
      `authorization.NewDecorator(...).AddAuditLogging().AddAnonymization()` so every decision lands
      in the audit log under a stable key.

### Identity Handling

- [ ] **Never serve a request under the virtual workspace's own privileged identity.**
      Decisions and downstream access must be attributable to the requesting user; where the
      implementation acts on behalf of the user, use the requester's identity or properly
      scoped impersonation.
- [ ] **Do not replay client-supplied impersonation.** If the implementation forwards
      requests, scrub impersonation headers first, so a caller's impersonation request
      is not re-evaluated against a more privileged forwarding identity.
      `authentication.ScrubImpersonationHeaders` is provided in virtual-workspace-framework for this purpose.

### Deployment and Interaction with Built-In Components

- [ ] **Treat the deployment as part of the trusted control plane.** It holds credentials with
      administrative access to shards; protect its kubeconfig and client certificates like
      shard credentials, and generally don't run it on infrastructure that tenants control.
- [ ] **Expose it through the kcp-front-proxy path mapping** (`--mapping-file`), not directly
      to tenants. The mapping carries a dedicated proxy client certificate and forwards the
      authenticated user via configurable user/group/extra headers.
- [ ] **Only trust forwarded identity headers over verified mTLS.** Accept the user, group,
      and extra headers exclusively from connections presenting a client certificate that
      chains to the configured request-header CA - otherwise any network peer can assert an
      arbitrary identity.

## Additional Best Practices

The following best practices are used inside kcp when building our virtual workspaces:

- [ ] **Ship end-to-end authorization tests, including negative cases.** At minimum: a
      request without the RBAC grant is refused, a request targeting a workspace without the
      qualifying relationship (e.g. no `APIBinding`) is refused, a request for a resource
      outside the view's scope is refused.
- [ ] **Document the endpoint for its users**: include the URL pattern, intended audience, and
      the exact RBAC permission required to access it.
