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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// APIExportIdentityRotation is a one-shot request to rotate an APIExport's
// identity onto a fresh identity secret. It lives next to the APIExport in
// the provider workspace and is served through the platform-owned
// migration APIExport: possessing the type requires binding that export,
// which is how the platform delegates the rotation capability.
//
// Rotation is a tracked, resumable procedure: every consumer workspace's
// bound instances are drained storage-level from the old identity prefix to
// the new one (preserving UIDs, status and ownerReferences), then the old
// identity survives only as an alias for permission claims until it is
// retired per spec.aliasRetirement.
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
type APIExportIdentityRotation struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec APIExportIdentityRotationSpec `json:"spec"`

	// +optional
	Status APIExportIdentityRotationStatus `json:"status,omitempty"`
}

// APIExportIdentityRotationSpec is the request: which export, which new
// identity, and when the old identity's alias is retired.
type APIExportIdentityRotationSpec struct {
	// export references the APIExport whose identity is rotated. The
	// referenced export may live in another workspace (and on another
	// shard): rotation is a platform capability, delegated by binding the
	// migration.kcp.io APIExport, and does not require exposing that
	// capability in the workspaces owning the rotated exports. Immutable.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="export is immutable"
	Export ExportReference `json:"export"`

	// newIdentity references the pre-created secret holding the new
	// identity key. Immutable.
	//
	// +required
	NewIdentity apisv1alpha2.Identity `json:"newIdentity"`

	// aliasRetirement decides when the old identity hash stops being
	// honored as an equivalent of the new one:
	//
	//   - Manual (default): the alias lives until the provider retires it by
	//     updating this field to Immediate (or After with an elapsed
	//     duration).
	//   - After: the alias is retired the given duration after the rotation
	//     entered AliasActive.
	//   - Immediate: no alias window; stale references break the moment the
	//     drain completes. This is the leaked-secret remediation mode.
	//
	// The field is mutable while the rotation is in AliasActive, but only
	// toward earlier retirement. Retirement is irreversible.
	//
	// +optional
	// +kubebuilder:default={policy: Manual}
	AliasRetirement AliasRetirement `json:"aliasRetirement,omitempty"`
}

// ExportReference identifies the APIExport being rotated.
type ExportReference struct {
	// path is the workspace path of the APIExport (e.g. root:org:provider,
	// or a logical cluster name). Empty means the rotation's own workspace.
	//
	// +optional
	Path string `json:"path,omitempty"`

	// name is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AliasRetirement configures when the pre-rotation identity alias is
// retired.
type AliasRetirement struct {
	// policy selects the retirement mode.
	//
	// +required
	// +kubebuilder:validation:Enum=Manual;After;Immediate
	Policy AliasRetirementPolicy `json:"policy"`

	// after is the deprecation window for the After policy, measured from
	// the rotation entering AliasActive.
	//
	// +optional
	After *metav1.Duration `json:"after,omitempty"`
}

// AliasRetirementPolicy is a retirement mode for the identity alias.
type AliasRetirementPolicy string

const (
	// AliasRetirementManual keeps the alias until the provider explicitly
	// retires it.
	AliasRetirementManual AliasRetirementPolicy = "Manual"
	// AliasRetirementAfter retires the alias a fixed duration after the
	// drain completed.
	AliasRetirementAfter AliasRetirementPolicy = "After"
	// AliasRetirementImmediate retires the alias the moment the drain
	// completes.
	AliasRetirementImmediate AliasRetirementPolicy = "Immediate"
)

// APIExportIdentityRotationPhase describes the lifecycle position of a
// rotation.
type APIExportIdentityRotationPhase string

const (
	// APIExportIdentityRotationPending: the rotation is validated and the
	// new identity computed, but no workspace has been drained yet.
	APIExportIdentityRotationPending APIExportIdentityRotationPhase = "Pending"
	// APIExportIdentityRotationMigrating: per-workspace drains are fanned
	// out across all bindings of the export.
	APIExportIdentityRotationMigrating APIExportIdentityRotationPhase = "Migrating"
	// APIExportIdentityRotationAliasActive: all data is drained; the old
	// hash lives on only as a claim alias until retirement.
	APIExportIdentityRotationAliasActive APIExportIdentityRotationPhase = "AliasActive"
	// APIExportIdentityRotationCompleted: the alias is retired, the old
	// identity is dead and its secret deletable.
	APIExportIdentityRotationCompleted APIExportIdentityRotationPhase = "Completed"
	// APIExportIdentityRotationFailed: the rotation cannot proceed; see
	// conditions.
	APIExportIdentityRotationFailed APIExportIdentityRotationPhase = "Failed"
)

// APIExportIdentityRotationStatus reports rotation progress.
type APIExportIdentityRotationStatus struct {
	// phase is the current lifecycle phase of the rotation.
	//
	// +optional
	Phase APIExportIdentityRotationPhase `json:"phase,omitempty"`

	// oldIdentityHash is the identity hash the export had when the rotation
	// started.
	//
	// +optional
	OldIdentityHash string `json:"oldIdentityHash,omitempty"`

	// newIdentityHash is the identity hash derived from the new identity
	// secret.
	//
	// +optional
	NewIdentityHash string `json:"newIdentityHash,omitempty"`

	// migratedBindings is the number of APIBindings whose bound instances
	// are fully drained onto the new identity.
	//
	// +optional
	MigratedBindings int32 `json:"migratedBindings,omitempty"`

	// totalBindings is the number of APIBindings bound to the rotating
	// export.
	//
	// +optional
	TotalBindings int32 `json:"totalBindings,omitempty"`

	// aliasActiveTimestamp records when the rotation entered AliasActive,
	// the reference point for the After retirement policy.
	//
	// +optional
	AliasActiveTimestamp *metav1.Time `json:"aliasActiveTimestamp,omitempty"`

	// conditions is a list of conditions that apply to the rotation.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

func (in *APIExportIdentityRotation) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *APIExportIdentityRotation) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// These are valid conditions of APIExportIdentityRotation.
const (
	// IdentityRotationValid indicates the rotation request passed
	// validation: the export exists, the new secret exists and hashes to a
	// new identity, and no conflicting rotation or binding migration is in
	// progress.
	IdentityRotationValid conditionsv1alpha1.ConditionType = "Valid"

	// IdentityRotationDrained indicates all bindings of the export are
	// drained onto the new identity.
	IdentityRotationDrained conditionsv1alpha1.ConditionType = "Drained"

	// IdentityRotationAliasRetired indicates the old identity alias has
	// been retired.
	IdentityRotationAliasRetired conditionsv1alpha1.ConditionType = "AliasRetired"
)

// APIExportIdentityRotationList is a list of APIExportIdentityRotation
// resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIExportIdentityRotationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIExportIdentityRotation `json:"items"`
}
