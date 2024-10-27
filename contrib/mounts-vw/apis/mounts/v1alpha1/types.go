package v1alpha1

const (

	// InternalWorkspaceProxyKeyLabel is an internal label set on a WorkspaceProxy resource that contains the full hash of the WorkspaceProxyKey, generated with the ToProxyTargetKey(..)
	// helper func, this label is used for reverse lookups of workspaceProxyKey to WorkspaceProxy.
	InternalWorkspaceProxyKeyLabel = "internal.mounts.contrib.kcp.io/key"
)
