// Package accessprovider defines the AccessProvider interface — the
// writer-side seam between the access graph and the various sources
// of authorization data that may populate it.
package accessprovider

import (
	"context"

	"github.com/kcp-dev/kcp/contrib/access-vw/pkg/graph"
)

// AccessProvider populates an access graph from some authorization source.
type AccessProvider interface {
	Start(ctx context.Context, g *graph.Graph) error
}
