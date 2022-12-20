/*
Copyright 2022 The KCP Authors.

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

package v1alpha1

type ResourceState string

const (
	// ResourceStatePending is the initial state of a resource after placement onto
	// a sync target. Either some workload controller or some external coordination
	// controller will set this to "Sync" when the resource is ready to be synced.
	ResourceStatePending ResourceState = ""
	// ResourceStateSync is the state of a resource when it is synced to the sync target.
	// This includes the deletion process until the resource is deleted downstream and the
	// syncer removes the state.workload.kcp.dev/<sync-target-name> label.
	ResourceStateSync ResourceState = "Sync"
	// ResourceStateUpsync is the state of a resource when it is synced up from the sync target.
	// Compared to Sync state, this state is exclusive, meaning that only one sync target can
	// be up-syncing a resource, and in addition, others sync targets cannot sync from this resource
	// because the up-syncer is owning both the spec and the status of that resource.
	ResourceStateUpsync ResourceState = "Upsync"
)

const (
	// InternalClusterDeletionTimestampAnnotationPrefix is the prefix of the annotation
	//
	//   deletion.internal.workload.kcp.dev/<sync-target-name>
	//
	// on upstream resources storing the timestamp when the sync target resource
	// state was changed to "Delete". The syncer will see this timestamp as the deletion
	// timestamp of the object.
	//
	// The format is RFC3339.
	//
	// TODO(sttts): use sync-target-uid instead of sync-target-name
	InternalClusterDeletionTimestampAnnotationPrefix = "deletion.internal.workload.kcp.dev/"

	// ClusterFinalizerAnnotationPrefix is the prefix of the annotation
	//
	//   finalizers.workload.kcp.dev/<sync-target-name>
	//
	// on upstream resources storing a comma-separated list of finalizer names that are set on
	// the sync target resource in the view of the syncer. This blocks the deletion of the
	// resource on that sync target. External (custom) controllers can set this annotation
	// create back-pressure on the resource.
	//
	// TODO(sttts): use sync-target-uid instead of sync-target-name
	ClusterFinalizerAnnotationPrefix = "finalizers.workload.kcp.dev/"

	// ClusterResourceStateLabelPrefix is the prefix of the label
	//
	//   state.workload.kcp.dev/<sync-target-name>
	//
	// on upstream resources storing the state of the sync target syncer state machine.
	// The workload controllers will set this label and the syncer will react and drive the
	// life-cycle of the synced objects on the sync target.
	//
	// The format is a string, namely:
	// - "": the object is assigned, but the syncer will ignore the object. A coordination
	//       controller will have to set the value to "Sync" after initializion in order to
	//       start the sync process.
	// - "Sync": the object is assigned and the syncer will start the sync process.
	//
	// While being in "Sync" state, a deletion timestamp in deletion.internal.workload.kcp.dev/<sync-target-name>
	// will signal the start of the deletion process of the object. During the deletion process
	// the object will stay in "Sync" state. The syncer will block deletion while
	// finalizers.workload.kcp.dev/<sync-target-name> exists and is non-empty, and it
	// will eventually remove state.workload.kcp.dev/<sync-target-name> after
	// the object has been deleted downstream.
	//
	// The workload controllers will consider the object deleted from the sync target when
	// the label is removed. They then set the placement state to "Unbound".
	ClusterResourceStateLabelPrefix = "state.workload.kcp.dev/"

	// InternalSyncerViewAnnotationPrefix is the prefix of the annotation
	//
	//   diff.syncer.internal.kcp.dev/<sync-target-key>
	//
	// on upstream resources storing the value of fields as they have been reported by the Syncer for the given SyncTarget,
	// so possibly different from the field value in the upstream resource itself, and overriding it for the given SyncTarget.
	//
	// The format is a Json object, whose keys are fields identifiers (for example "status" or "spec.clusterIP"),
	// and values are overriding field values.
	InternalSyncerViewAnnotationPrefix = "diff.syncer.internal.kcp.dev/"

	// InternalClusterStatusAnnotationPrefix is the prefix of the annotation
	//
	//   experimental.status.workload.kcp.dev/<sync-target-name>
	//
	// on upstream resources storing the status of the downstream resource per sync target.
	// Note that this is experimental and will disappear in the future without prior notice. It
	// is used temporarily in the case that a resource is scheduled to multiple sync targets.
	//
	// The format is JSON.
	InternalClusterStatusAnnotationPrefix = "experimental.status.workload.kcp.dev/"

	// ClusterSpecDiffAnnotationPrefix is the prefix of the annotation
	//
	//   experimental.spec-diff.workload.kcp.dev/<sync-target-name>
	//
	// on upstream resources storing the desired spec diffs to be applied to the resource when syncing
	// down to the <sync-target-name>. This feature requires the "Advanced Scheduling" feature gate
	// to be enabled.
	//
	// The patch will be applied to the resource Spec field of the resource, so the JSON root path is the
	// resource's Spec field.
	//
	// The format for the value of this annotation is: JSON Patch (https://tools.ietf.org/html/rfc6902).
	ClusterSpecDiffAnnotationPrefix = "experimental.spec-diff.workload.kcp.dev/"

	// ExperimentalSummarizingRulesAnnotation
	//
	//   experimental.summarizing.workload.kcp.dev
	//
	// on upstream resources storing the JSON-encoded summarizing rules for this instance of the resource.
	// The drives what fields should be overridden in the syncer view, and available for summarizing,
	// and how they should be managed.
	//
	// To express that only the "status" field should be summarized, and promoted to the upstream
	// resource when scheduled on only 1 SyncTarget, the annotation would be:
	//
	//    [{"fieldPath": "status", "promoteToUpstream": true}]
	//
	// The format for the value of this annotation is a JSON Array of objects with 2 fields:
	//   - fieldPath: defines that path (dot-separated) of a field that should be summarized
	//   - promoteToUpstream: defines whether this field should be promoted to upstream when the
	//     resource is scheduled to only one SyncTarget.
	ExperimentalSummarizingRulesAnnotation = "experimental.summarizing.workload.kcp.dev"

	// InternalDownstreamClusterLabel is a label with the upstream cluster name applied on the downstream cluster
	// instead of state.workload.kcp.dev/<sync-target-name> which is used upstream.
	InternalDownstreamClusterLabel = "internal.workload.kcp.dev/cluster"

	// AnnotationSkipDefaultObjectCreation is the annotation key for an apiexport or apibinding indicating the other default resources
	// has been created already. If the created default resource is deleted, it will not be recreated.
	AnnotationSkipDefaultObjectCreation = "workload.kcp.dev/skip-default-object-creation"

	// InternalSyncTargetPlacementAnnotationKey is an internal annotation key on placement API to mark the synctarget scheduled
	// from this placement. The value is a hash of the SyncTarget cluster name + SyncTarget name, generated with the ToSyncTargetKey(..) helper func.
	InternalSyncTargetPlacementAnnotationKey = "internal.workload.kcp.dev/synctarget"

	// InternalSyncTargetKeyLabel is an internal label set on a SyncTarget resource that contains the full hash of the SyncTargetKey, generated with the ToSyncTargetKey(..)
	// helper func, this label is used for reverse lookups of a syncTargetKey to SyncTarget.
	InternalSyncTargetKeyLabel = "internal.workload.kcp.dev/key"

	// ComputeAPIExportAnnotationKey is an annotation key set on an APIExport when it will be used for compute,
	// and its APIs are expected to be synced to a SyncTarget by the Syncer. The annotation will be continuously
	// synced from the APIExport to all the APIBindings bound to this APIExport. The workload scheduler will
	// check all the APIBindings with this annotation for scheduling purpose.
	ComputeAPIExportAnnotationKey = "extra.apis.kcp.dev/compute.workload.kcp.dev"
)
