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
	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// enqueueAPIBinding re-queues the claiming APIExports whose identity-agnostic
// claims may start or stop being served because of a change to this binding:
//
//   - the binding itself accepts an identity-agnostic claim, so the export it
//     points at gains or loses a consumer;
//   - the binding serves (bound resources) a group/resource that some binding
//     in the same workspace claims identity-agnostically, so those claiming
//     exports gain or lose a producer identity to resolve through.
func (c *APIReconciler) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = tombstone.Obj
	}
	binding, ok := obj.(*apisv1alpha2.APIBinding)
	if !ok {
		return
	}
	logger = logging.WithObject(logger, binding).WithValues("reason", "APIBinding change")

	if acceptsIdentityAgnosticClaim(binding) {
		c.enqueueExportsByReference(binding, logger)
	}

	cluster := logicalcluster.From(binding)
	for _, bound := range binding.Status.BoundResources {
		gr := schema.GroupResource{Group: bound.Group, Resource: bound.Resource}
		claimers, err := indexers.ListAPIBindingsByAcceptedClaimedGroupResource(c.apiBindingIndexer, gr)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}
		for _, claimer := range claimers {
			if logicalcluster.From(claimer) != cluster || !acceptsIdentityAgnosticClaimFor(claimer, gr) {
				continue
			}
			c.enqueueExportsByReference(claimer, logger.WithValues("boundResource", gr.String()))
		}
	}
}

// enqueueExportsByReference queues the APIExport a binding references. The
// reference may use the canonical path or the cluster name; the
// ByLogicalClusterPathAndName index answers both.
func (c *APIReconciler) enqueueExportsByReference(binding *apisv1alpha2.APIBinding, logger logr.Logger) {
	if binding.Spec.Reference.Export == nil {
		return
	}
	path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = logicalcluster.From(binding).Path()
	}
	key := path.Join(binding.Spec.Reference.Export.Name).String()
	exports, err := indexers.ByIndex[*apisv1alpha2.APIExport](c.apiExportIndexer, indexers.ByLogicalClusterPathAndName, key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, export := range exports {
		c.enqueueAPIExport(export, logger)
	}
}

func acceptsIdentityAgnosticClaim(binding *apisv1alpha2.APIBinding) bool {
	for _, claim := range binding.Spec.PermissionClaims {
		if claim.State == apisv1alpha2.ClaimAccepted && claim.IdentityHash == "" {
			return true
		}
	}
	return false
}

func acceptsIdentityAgnosticClaimFor(binding *apisv1alpha2.APIBinding, gr schema.GroupResource) bool {
	for _, claim := range binding.Spec.PermissionClaims {
		if claim.State == apisv1alpha2.ClaimAccepted && claim.IdentityHash == "" && claim.Group == gr.Group && claim.Resource == gr.Resource {
			return true
		}
	}
	return false
}
