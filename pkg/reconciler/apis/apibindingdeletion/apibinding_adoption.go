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

package apibindingdeletion

import (
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// retentionReason explains why instances of a bound resource are kept in
// storage when their APIBinding is deleted.
type retentionReason string

const (
	// retainedAdopted means another APIBinding in the workspace references an
	// APIExport serving the same group/resource with the same APIResourceSchema
	// (by UID) and the same identity. That binding takes the instances over;
	// they are served again as soon as it binds.
	retainedAdopted retentionReason = "Adopted"

	// retainedOrphaned means spec.deletionPolicy is Orphan and no successor
	// exists. Instances stay in storage, unreachable until a binding with the
	// same schema and identity binds the group/resource again.
	retainedOrphaned retentionReason = "Orphaned"
)

type retention struct {
	reason retentionReason
	// successor is the name of the adopting APIBinding, set for retainedAdopted.
	successor string
}

// retainedResources decides, per bound group/resource, whether instances must
// be kept in storage when the given APIBinding is deleted. Resources with a
// verified successor are retained regardless of deletionPolicy; resources
// without one are retained only under deletionPolicy=Orphan.
//
// Successor verification is same-storage only: the successor's APIExport must
// serve the group/resource through the same APIResourceSchema (by UID) with
// the same identity hash. Lookups go through (possibly lagging) informers, so
// a just-created successor can be missed; deletionPolicy=Orphan is the
// belt-and-braces guard against that race.
func (c *Controller) retainedResources(clusterName logicalcluster.Name, apibinding *apisv1alpha2.APIBinding) (map[schema.GroupResource]retention, error) {
	retained := map[schema.GroupResource]retention{}

	bindings, err := c.listAPIBindings(clusterName)
	if err != nil {
		return nil, err
	}
	candidates := make([]*apisv1alpha2.APIBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.Name == apibinding.Name || !b.DeletionTimestamp.IsZero() {
			continue
		}
		candidates = append(candidates, b)
	}

	orphan := apibinding.Spec.DeletionPolicy == apisv1alpha2.APIBindingDeletionPolicyOrphan
	for _, br := range apibinding.Status.BoundResources {
		gr := schema.GroupResource{Group: br.Group, Resource: br.Resource}
		if successor, ok := c.successorFor(clusterName, candidates, br); ok {
			retained[gr] = retention{reason: retainedAdopted, successor: successor}
			continue
		}
		if orphan {
			retained[gr] = retention{reason: retainedOrphaned}
		}
	}

	return retained, nil
}

// successorFor returns the name of an APIBinding among candidates whose
// referenced APIExport serves the given bound resource through the same
// APIResourceSchema (by UID) with the same identity hash.
func (c *Controller) successorFor(clusterName logicalcluster.Name, candidates []*apisv1alpha2.APIBinding, br apisv1alpha2.BoundAPIResource) (string, bool) {
	for _, b := range candidates {
		if b.Spec.Reference.Export == nil {
			continue
		}
		path := logicalcluster.NewPath(b.Spec.Reference.Export.Path)
		if path.Empty() {
			path = clusterName.Path()
		}
		export, err := c.getAPIExportByPath(path, b.Spec.Reference.Export.Name)
		if err != nil {
			// A candidate whose export cannot be resolved cannot be verified as
			// same-storage; treating it as a successor would block deletion on
			// dangling references forever.
			continue
		}
		if export.Status.IdentityHash != br.Schema.IdentityHash {
			continue
		}
		for _, res := range export.Spec.Resources {
			if res.Group != br.Group || res.Name != br.Resource {
				continue
			}
			sch, err := c.getAPIResourceSchema(logicalcluster.From(export), res.Schema)
			if err != nil {
				continue
			}
			if string(sch.UID) == br.Schema.UID {
				return b.Name, true
			}
		}
	}

	return "", false
}
