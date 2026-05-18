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

package shared

import (
	"fmt"

	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

// ResolveLifecycleWorkspaceType resolves the WorkspaceType referenced by an
// initializer or terminator identifier.
//
// role is a human-readable label ("initializer" or "terminator") used only for
// error messages. id is the string form of the identifier. typeFrom is the
// per-lifecycle parser (e.g. initialization.TypeFrom / termination.TypeFrom)
// that splits id into (wstCluster, wstName). Encapsulating these as parameters
// keeps the two callers identical without forcing a tight coupling between
// the initializing and terminating packages.
//
// Returns:
//
//   - (wst, nil)  — id encodes a WST reference and the WST was found in the
//     local or cache-server indexer; the caller should evaluate the WST's
//     lifecycle permissions (Mode 1).
//   - (nil, nil)  — id is a known system initializer/terminator (e.g.
//     "system:apibindings"): well-formed but intentionally not backed by a
//     WST. Caller should fall through to owner impersonation (Mode 2).
//   - (nil, err)  — id is malformed or it encodes a real (non-system) WST
//     reference that neither indexer has. Both cases must fail closed:
//     granting Mode 2 to a lifecycle controller we cannot validate would be a
//     silent privilege escalation.
func ResolveLifecycleWorkspaceType(
	role string,
	id string,
	typeFrom func() (logicalcluster.Name, string, error),
	localWSTIndexer, cachedWSTIndexer cache.Indexer,
) (*tenancyv1alpha1.WorkspaceType, error) {
	wstCluster, wstName, typeErr := typeFrom()
	if typeErr != nil {
		return nil, fmt.Errorf("malformed %s %q: %w", role, id, typeErr)
	}
	if wstCluster == core.SystemCluster {
		return nil, nil //nolint:nilnil // system lifecycle controller: intentional fall-through to owner impersonation
	}
	return indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](
		tenancyv1alpha1.Resource("workspacetypes"),
		localWSTIndexer, cachedWSTIndexer,
		wstCluster.Path(), wstName,
	)
}
