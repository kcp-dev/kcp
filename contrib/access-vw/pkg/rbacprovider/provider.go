package rbacprovider

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/accessprovider"
	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

// Compile-time check that Provider satisfies the AccessProvider
// contract. If the interface ever changes, this line breaks first
// and we hear about it before any caller does.
var _ accessprovider.AccessProvider = (*Provider)(nil)

// Provider is the kcp-native RBAC AccessProvider.
//
// On Start, it stands up cross-shard informers on
// ClusterRoleBinding and RoleBinding (RBAC verb-level filtering
// against (Cluster)Role rules is a follow-up; the MVP treats any
// binding as "view"), translates events into graph mutations via a
// Translator, and marks the graph Ready once the initial sync
// completes.
//
// Provider supports three modes, picked at Start time from the
// fields set on the struct:
//
//   - Multi-shard (preferred): RestConfig and APIExportEndpointSlice
//     are both set. Start brings up a multicluster-runtime manager
//     backed by the kcp apiexport provider, and runs CRB/RB
//     reconcilers across every workspace that has an APIBinding to
//     the access VW's APIExport. This is the production wiring.
//   - Single-shard: RestConfig is set, APIExportEndpointSlice is
//     empty. Start uses standard client-go informers against one
//     shard. Useful for development against a non-kcp Kubernetes
//     cluster, or for a kcp setup with a single shard exposed on the
//     supplied REST config.
//   - Stub mode: RestConfig is nil. Start builds the translator,
//     marks the graph Ready (with an empty data set), and blocks on
//     ctx. Useful for the demo binary. Tests that want to drive
//     translation directly construct a NewTranslator instead of going
//     through a Provider.
type Provider struct {
	// EndpointBaseURL is the FrontProxy URL prefix used to construct
	// each cluster's endpoint. Per-cluster URL is base + cluster name.
	EndpointBaseURL string

	// RestConfig is the Kubernetes/kcp REST config the provider
	// talks to. When nil, Provider runs in stub mode. In multi-shard
	// mode this should point at the kcp root shard so the apiexport
	// virtual workspace can be reached.
	RestConfig *rest.Config

	// APIExportEndpointSlice is the name of the APIExportEndpointSlice
	// object the apiexport multicluster provider should follow. When
	// non-empty, Start runs in multi-shard mode. Empty means single-
	// shard (or stub if RestConfig is also nil).
	APIExportEndpointSlice string

	translator *Translator
}

// New returns a configured Provider in stub mode.
func New(endpointBaseURL string) *Provider {
	return &Provider{EndpointBaseURL: endpointBaseURL}
}

// Start implements accessprovider.AccessProvider. It dispatches to
// one of three execution modes (multi-shard, single-shard, stub).
func (p *Provider) Start(ctx context.Context, g *graph.Graph) error {
	p.translator = NewTranslator(g)

	switch {
	case p.RestConfig != nil && p.APIExportEndpointSlice != "":
		return p.runMulticluster(ctx, p.RestConfig, g)

	case p.RestConfig != nil:
		client, err := kubernetes.NewForConfig(p.RestConfig)
		if err != nil {
			return fmt.Errorf("build kubernetes client: %w", err)
		}
		return p.runInformers(ctx, client, g)

	default:
		g.SetReady()
		<-ctx.Done()
		return nil
	}
}

func (p *Provider) endpointFor(c graph.LogicalCluster) string {
	base := p.EndpointBaseURL
	if base != "" && !strings.HasSuffix(base, "/") {
		base += "/"
	}

	return base + string(c)
}
