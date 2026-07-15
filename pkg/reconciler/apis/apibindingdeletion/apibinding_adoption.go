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

// adoptedResources returns, per bound group/resource of the deleting
// APIBinding, the name of another APIBinding in the same workspace that will
// take the instances over. Instances of an adopted resource must not be
// deleted when this APIBinding is deleted: the successor serves them
// unchanged.
//
// A binding is a successor for a bound resource only if its referenced
// APIExport serves the same group/resource through the same APIResourceSchema
// (by UID) with the same identity hash. That guarantees identical storage
// (same etcd prefix) and identical served API (same bound CRD), so the
// handover moves no data.
//
// Lookups go through (possibly lagging) informers. A just-created successor
// can be missed on one pass; the deleting binding is requeued and re-evaluated
// while its instances remain, so a transient miss only delays the handover.
func (c *Controller) adoptedResources(clusterName logicalcluster.Name, apibinding *apisv1alpha2.APIBinding) (map[schema.GroupResource]string, error) {
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

	adopted := map[schema.GroupResource]string{}
	for _, br := range apibinding.Status.BoundResources {
		if successor, ok := c.successorFor(clusterName, candidates, br); ok {
			adopted[schema.GroupResource{Group: br.Group, Resource: br.Resource}] = successor
		}
	}

	return adopted, nil
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
			// same-storage; skip it rather than block deletion on a dangling
			// reference.
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
