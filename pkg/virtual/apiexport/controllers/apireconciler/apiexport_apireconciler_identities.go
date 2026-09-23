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

package apireconciler

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// staticIdentities returns an identities func for a resource stored under one
// fixed identity hash. An empty hash yields nil, which the forwarding registry
// treats as "no identity": the resource name is forwarded as-is.
func staticIdentities(identityHash string) forwardingregistry.IdentityHashesFunc {
	if identityHash == "" {
		return nil
	}
	return func(context.Context) []string {
		return []string{identityHash}
	}
}

// dynamicIdentities returns an identities func for an identity-agnostic claim:
// on every request it re-derives, from this shard's APIBindings, the set of
// identities under which the claimed resource is bound in the consumer
// workspaces that accepted the claim. Rotation and multiple genuine producers
// are therefore followed without rebuilding the serving storage.
func (c *APIReconciler) dynamicIdentities(apiExport *apisv1alpha2.APIExport, gr schema.GroupResource) forwardingregistry.IdentityHashesFunc {
	// Only the stable coordinates of the export are captured, not the informer
	// object itself, so a stale copy can never pin an old identity.
	exportCluster := logicalcluster.From(apiExport)
	exportName := apiExport.Name
	return func(context.Context) []string {
		export, err := c.apiExportLister.Cluster(exportCluster).Get(exportName)
		if err != nil {
			utilruntime.HandleError(err)
			return []string{}
		}
		hashes, err := c.identityResolver.IdentityHashes(export, gr)
		if err != nil {
			utilruntime.HandleError(err)
			return []string{}
		}
		return hashes
	}
}

// findClaimedSchema returns the APIResourceSchema serving gr among the exports
// that carry identityHash, or nil if none does. Multiple exports may share an
// identity (same owner); they are visited in a deterministic order and the
// last match wins, mirroring the identity-hash claim path.
func (c *APIReconciler) findClaimedSchema(ctx context.Context, identityHash string, gr schema.GroupResource) (*apisv1alpha1.APIResourceSchema, error) {
	logger := klog.FromContext(ctx).WithValues("identity", identityHash)

	exports, err := indexers.ByIndex[*apisv1alpha2.APIExport](c.apiExportIndexer, indexers.APIExportByIdentity, identityHash)
	if err != nil {
		return nil, err
	}
	sort.Slice(exports, func(i, j int) bool {
		if exports[i].Name != exports[j].Name {
			return exports[i].Name < exports[j].Name
		}
		return logicalcluster.From(exports[i]).String() < logicalcluster.From(exports[j]).String()
	})

	var match *apisv1alpha1.APIResourceSchema
	for _, export := range exports {
		logger := logger.WithValues(logging.FromPrefix("candidateAPIExport", export)...)
		candidates, err := c.getSchemasFromAPIExport(ctx, export)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			if candidate.Spec.Group != gr.Group || candidate.Spec.Names.Plural != gr.Resource {
				continue
			}
			logger.V(4).Info("found APIResourceSchema for claimed resource", "schema", candidate.Name)
			match = candidate
		}
	}
	return match, nil
}
