/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
