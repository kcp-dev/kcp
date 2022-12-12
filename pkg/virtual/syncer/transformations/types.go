/*
Copyright 2021 The KCP Authors.

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

package transformations

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/pkg/apis/workload/helpers"
)

// Transformation defines the action of transforming an resource when exposing it
// to the Syncer for a given SyncTarget through the Syncer Virtual Workspace.
// That's why the transformed resource is called the Syncer View.
//
// In addition to the upstream resource to transform, the transformation parameters
// also involve:
//   - the overriding values of Syncer View summarized fields (the fields that the
//
// Syncer previously overrode, typically when updating the status, but this could
// also contain specific Spec fields)
//   - the requested syncing intents for all SyncTargets.
type Transformation interface {
	ToSyncerView(SyncTargetKey string, gvr schema.GroupVersionResource, upstreamResource *unstructured.Unstructured, overridenSyncerViewFields map[string]interface{}, requestedSyncing map[string]helpers.SyncIntent) (newSyncerViewResource *unstructured.Unstructured, err error)
}

// TransformationProvider provides an appropriate Transformation based on the content of a resource
type TransformationProvider interface {
	TransformationFor(resource metav1.Object) (Transformation, error)
}

// SummarizingRules defines rules that drive the way some specified fields
// (typically the status, but not limited to it), when updated by the Syncer,
// are managed by the Syncer Virtual Workspace with 2 possible options:
//   - either stored in the SyncerView (as overriding field values in an annotation)
//   - or promoted to the upstream object itself.
type SummarizingRules interface {
	FieldsToSummarize(gvr schema.GroupVersionResource) []FieldToSummarize
}

// SummarizingRulesProvider provides appropriate SummarizingRules based on the content of a resource
type SummarizingRulesProvider interface {
	SummarizingRulesFor(resource metav1.Object) (SummarizingRules, error)
}

// FieldToSummarize defines a Field that can be overridden by the Syncer for a given Synctarget,
// as well as the rules according to which it will be managed.
type FieldToSummarize interface {
	Field
	FieldSummarizingRules
}

// Field defines a Field that can be overridden by the Syncer for a given Synctarget.
// This is an interface since they would be several ways to identify a field.
// First implementation is very limited and doesn't support lists (ex: "status", "spec.ClusterIP").
// It could also support JsonPath.
type Field interface {
	// Set allows setting the value of this field on a resource.
	Set(resource *unstructured.Unstructured, value interface{}) error
	// Get allows getting the value of this field from a resource.
	// The retrieved value should be deep-copied before mutation.
	Get(resource *unstructured.Unstructured) (interface{}, bool, error)
	// Delete allows deleting this field from a resource
	Delete(resource *unstructured.Unstructured)
	// Path provides the path of this field, which will be used as the key
	// in the map of overriding field values (for a SyncTarget),
	// stored on the resource as an annotation.
	Path() string
}

// FieldSummarizingRules defines rules according to which the summarized field
// should be managed.
// More rules might be added in the future.
type FieldSummarizingRules interface {
	// IsStatus if the field is part of the status sub-resource.
	// This is important, since it drives how the field will be updated
	// during the transformation (according to the subresource
	// of the Update / Get action).
	IsStatus() bool
	// CanPromoteToUpstream defines if the field should be promoted to the upstream resource
	// (when the resource is scheduled on only one Synctarget of course).
	// Promoted fields are always owned by the Syncer.
	CanPromoteToUpstream() bool
}
