package server

const (
	PassthroughHeader   = "X-Kcp-Api-V1-Discovery-Passthrough"
	WorkspaceAnnotation = "tenancy.kcp.dev/workspace"
)

type (
	acceptHeaderContextKeyType int
	userAgentContextKeyType    int
)

const (
	// acceptHeaderContextKey is the context key for the request namespace.
	acceptHeaderContextKey acceptHeaderContextKeyType = iota

	// userAgentContextKey is the context key for the request user-agent.
	userAgentContextKey userAgentContextKeyType = iota
)
