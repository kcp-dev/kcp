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

package v1alpha1

import (
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// ActiveRekeyAnnotationKey is set on an APIExport while a storage re-keying
// is draining its bindings. The value is
// "<logical cluster name>|<rekey name>|<target storage key>|<resources>" of
// the APIExportStorageRekey, where <resources> is a comma-separated list of
// "<resource>.<group>" entries limiting the re-keying to those resources, or
// empty for every CRD-stored resource of the export. The export is
// replicated to the cache server, so the annotation is how the per-shard
// storage migrators find the re-keying object to report their drain
// progress to, and how the apibinding reconcilers learn which prefix the
// bindings have to move to. Cleared when the drain is complete.
const ActiveRekeyAnnotationKey = "migration.kcp.io/active-rekey"

// ActiveRekey is the parsed value of the ActiveRekeyAnnotationKey annotation.
type ActiveRekey struct {
	// Cluster is the logical cluster name the re-keying object lives in.
	Cluster string
	// Name is the name of the re-keying object.
	Name string
	// TargetKey is the storage key the bindings are drained onto.
	TargetKey string
	// Resources limits the re-keying to the listed group/resources. Empty
	// means every CRD-stored resource of the export.
	Resources []apisv1alpha2.GroupResource
}

// ParseActiveRekey parses an ActiveRekeyAnnotationKey annotation value. It
// returns false for an empty or malformed value.
func ParseActiveRekey(value string) (ActiveRekey, bool) {
	if value == "" {
		return ActiveRekey{}, false
	}
	parts := strings.Split(value, "|")
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ActiveRekey{}, false
	}
	rekey := ActiveRekey{Cluster: parts[0], Name: parts[1], TargetKey: parts[2]}
	if len(parts) > 3 && parts[3] != "" {
		for _, entry := range strings.Split(parts[3], ",") {
			resource, group, _ := strings.Cut(entry, ".")
			rekey.Resources = append(rekey.Resources, apisv1alpha2.GroupResource{Group: group, Resource: resource})
		}
	}
	return rekey, true
}

// String renders the annotation value.
func (r ActiveRekey) String() string {
	resources := make([]string, 0, len(r.Resources))
	for _, gr := range r.Resources {
		resources = append(resources, gr.Resource+"."+gr.Group)
	}
	return strings.Join([]string{r.Cluster, r.Name, r.TargetKey, strings.Join(resources, ",")}, "|")
}

// Covers reports whether the re-keying applies to the given group/resource.
func (r ActiveRekey) Covers(group, resource string) bool {
	if len(r.Resources) == 0 {
		return true
	}
	for _, gr := range r.Resources {
		if gr.Group == group && gr.Resource == resource {
			return true
		}
	}
	return false
}

// APIExportStorageRekey is a one-shot request to move the bound instances of
// an APIExport's resources onto another etcd storage prefix. It lives next
// to the APIExport in the provider workspace, or in a platform-operated
// workspace referencing the export by path, and is served through the
// platform-owned migration APIExport: possessing the type requires binding
// that export, which is how the platform delegates the capability.
//
// The identity of the export never changes. Only the storage prefix of the
// bound instances does: by default the export's own identity, which is the
// steady state, so a re-keying ends a storage handover (an export serving a
// resource out of another export's prefix) or separates prefixes after one.
//
// Re-keying is a tracked, resumable procedure: every consumer workspace's
// bound instances are drained storage-level from the current prefix to the
// target one (preserving UIDs, status and ownerReferences), one workspace at
// a time, behind the workspace's maintenance fence.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Export",type="string",JSONPath=".spec.export.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Migrated",type="string",JSONPath=".status.migratedBindings"
// +kubebuilder:printcolumn:name="Total",type="string",JSONPath=".status.totalBindings"
type APIExportStorageRekey struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec APIExportStorageRekeySpec `json:"spec"`

	// +optional
	Status APIExportStorageRekeyStatus `json:"status,omitempty"`
}

// APIExportStorageRekeySpec is the request: which export, which of its
// resources, and onto which storage key.
type APIExportStorageRekeySpec struct {
	// export references the APIExport whose bound instances are re-keyed.
	// The referenced export may live in another workspace (and on another
	// shard): re-keying is a platform capability, delegated by binding the
	// migration.kcp.io APIExport, and does not require exposing that
	// capability in the workspaces owning the re-keyed exports. Immutable.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="export is immutable"
	Export ExportReference `json:"export"`

	// resources limits the re-keying to the listed group/resources of the
	// export. Empty means every CRD-stored resource of the export whose
	// bindings sit on a prefix other than the target. Immutable.
	//
	// +optional
	// +listType=map
	// +listMapKey=group
	// +listMapKey=resource
	Resources []apisv1alpha2.GroupResource `json:"resources,omitempty"`

	// targetKey is the storage key to drain onto. Empty means the export's
	// own identity, which is the steady state. Any other value is reserved
	// for platform administrators, e.g. to move onto a well-known core
	// identity. It is a single etcd path segment. Immutable.
	//
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^([a-z0-9][a-z0-9.-]*)?$`
	TargetKey string `json:"targetKey,omitempty"`
}

// ExportReference identifies the APIExport being re-keyed.
type ExportReference struct {
	// path is the workspace path of the APIExport (e.g. root:org:provider,
	// or a logical cluster name). Empty means the re-keying's own workspace.
	//
	// +optional
	Path string `json:"path,omitempty"`

	// name is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// APIExportStorageRekeyPhase describes the lifecycle position of a
// re-keying.
type APIExportStorageRekeyPhase string

const (
	// APIExportStorageRekeyPending: the request is validated and the target
	// key resolved, but no workspace has been drained yet.
	APIExportStorageRekeyPending APIExportStorageRekeyPhase = "Pending"
	// APIExportStorageRekeyDraining: per-workspace drains are fanned out
	// across all bindings of the export.
	APIExportStorageRekeyDraining APIExportStorageRekeyPhase = "Draining"
	// APIExportStorageRekeyCompleted: every binding of the export serves
	// from the target key and the source prefixes are gone.
	APIExportStorageRekeyCompleted APIExportStorageRekeyPhase = "Completed"
	// APIExportStorageRekeyFailed: the re-keying cannot proceed; see
	// conditions.
	APIExportStorageRekeyFailed APIExportStorageRekeyPhase = "Failed"
)

// APIExportStorageRekeyStatus reports drain progress.
type APIExportStorageRekeyStatus struct {
	// phase is the current lifecycle phase of the re-keying.
	//
	// +optional
	Phase APIExportStorageRekeyPhase `json:"phase,omitempty"`

	// targetKey is the resolved storage key the bindings are drained onto.
	//
	// +optional
	TargetKey string `json:"targetKey,omitempty"`

	// migratedBindings is the number of APIBindings whose bound instances
	// are fully drained onto the target key, summed over all shards that
	// have reported in status.shards.
	//
	// +optional
	MigratedBindings int32 `json:"migratedBindings,omitempty"`

	// totalBindings is the number of APIBindings bound to the re-keyed
	// export, summed over all shards that have reported in status.shards.
	//
	// +optional
	TotalBindings int32 `json:"totalBindings,omitempty"`

	// shards reports drain progress per shard. Each shard's storage
	// migrator maintains its own entry (including shards with zero
	// bindings, so completeness is decidable). The drain is only considered
	// complete once every shard has reported and every entry is fully
	// migrated.
	//
	// +optional
	// +listType=map
	// +listMapKey=shard
	Shards []ShardMigrationProgress `json:"shards,omitempty"`

	// conditions is a list of conditions that apply to the re-keying.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ShardMigrationProgress is one shard's self-reported drain progress for a
// re-keying. It is written by the storage migrator running on that shard via
// server-side apply, with a per-shard field manager, so shards never conflict
// with each other or with the re-keying controller.
type ShardMigrationProgress struct {
	// shard is the name of the reporting shard.
	//
	// +required
	Shard string `json:"shard"`

	// storageKey is the target key the counts were evaluated against.
	// Reports for any other key than the re-keying's target are ignored by
	// the re-keying controller.
	//
	// +optional
	StorageKey string `json:"storageKey,omitempty"`

	// totalBindings is the number of APIBindings of the re-keyed export
	// hosted on this shard. Zero is meaningful ("this shard has nothing to
	// drain") and always serialized.
	//
	// +required
	TotalBindings int32 `json:"totalBindings"`

	// migratedBindings is the number of those bindings fully drained onto
	// the target key.
	//
	// +required
	MigratedBindings int32 `json:"migratedBindings"`

	// lastUpdateTime is when this shard last refreshed its entry.
	//
	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
}

func (in *APIExportStorageRekey) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExportStorageRekey) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of APIExportStorageRekey.
const (
	// StorageRekeyValid indicates the request passed validation: the export
	// exists, has an identity, the target key resolved, and no conflicting
	// re-keying is in progress.
	StorageRekeyValid conditionsv1alpha1.ConditionType = "Valid"

	// StorageRekeyDrained indicates all bindings of the export are drained
	// onto the target key.
	StorageRekeyDrained conditionsv1alpha1.ConditionType = "Drained"
)

// APIExportStorageRekeyList is a list of APIExportStorageRekey resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportStorageRekeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExportStorageRekey `json:"items"`
}
