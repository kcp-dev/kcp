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

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// CoreIdentitySet records which identities a shard uses for the core
// APIExports (tenancy.kcp.io, topology.kcp.io, shards.core.kcp.io,
// migration.kcp.io, cache.kcp.io) and where each of them came from.
//
// Every shard owns exactly one CoreIdentitySet, named after the shard, in its
// shard-local system:shard logical cluster. Only the owning shard writes it.
// It has no spec: it is a report, not a request. The object is replicated to
// the cache server and every shard's object is visible in the Admin
// workspace (kubectl ws use :admin), so an administrator sees the identities
// of the whole installation in one list.
//
// The identity hashes recorded here are the same plain SHA-256 hex strings
// that appear in request URLs (workspaces:<hash>) and storage keys. The key
// ID and derivation that produced an identity are recorded next to it, not
// inside it.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="KeyID",type=string,JSONPath=`.status.keyID`,description="The active core identities key"
// +kubebuilder:printcolumn:name="Observed",type=string,JSONPath=`.status.observedKeyID`,description="The core identities key currently mounted on the shard"
// +kubebuilder:printcolumn:name="Consistent",type=string,JSONPath=`.status.conditions[?(@.type=="Consistent")].status`,description="Whether the shard agrees with the other shards on the core identities"
// +kubebuilder:printcolumn:name="KeyChanged",type=string,JSONPath=`.status.conditions[?(@.type=="KeyChanged")].status`,description="Whether the mounted key differs from the active key"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type CoreIdentitySet struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Status CoreIdentitySetStatus `json:"status,omitempty"`
}

// CoreIdentitySetStatus is what the owning shard reports.
type CoreIdentitySetStatus struct {
	// shard is the name of the shard this object belongs to.
	//
	// +optional
	Shard string `json:"shard,omitempty"`

	// keyID identifies the active core identities key: the first key the
	// shard observed. It is a SHA-256 hex digest of the key's public key
	// (SubjectPublicKeyInfo). Empty when the shard has never had a key.
	//
	// +optional
	KeyID string `json:"keyID,omitempty"`

	// observedKeyID identifies the key currently mounted on the shard via
	// --core-identities-key-file. Empty when no key is mounted. It differs
	// from keyID when the key was changed after it was first recorded.
	//
	// +optional
	ObservedKeyID string `json:"observedKeyID,omitempty"`

	// exports lists the core APIExports and the identity this shard uses
	// for each of them, sorted by name.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	Exports []CoreIdentity `json:"exports,omitempty"`

	// conditions is a list of conditions that apply to the CoreIdentitySet.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// CoreIdentity is the identity a shard uses for one core APIExport.
type CoreIdentity struct {
	// name is the name of the APIExport in the root workspace, e.g.
	// tenancy.kcp.io.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// identityHash is the identity hash the shard uses for this export: the
	// SHA-256 hex digest of the identity key, as it appears in request URLs
	// and storage keys.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	IdentityHash string `json:"identityHash"`

	// source says where the identity came from.
	//
	// +required
	// +kubebuilder:validation:Enum=Legacy;Key;DevFile
	Source CoreIdentitySource `json:"source"`

	// keyID identifies the key the identity was derived from. Empty unless
	// source is Key.
	//
	// +optional
	KeyID string `json:"keyID,omitempty"`

	// derivation names the algorithm and version that derived the identity
	// from the key, e.g. hkdf-sha256/v1. Empty unless source is Key.
	//
	// +optional
	Derivation string `json:"derivation,omitempty"`
}

// CoreIdentitySource says where a core identity came from.
type CoreIdentitySource string

const (
	// CoreIdentitySourceLegacy marks an identity that was created randomly
	// by the root shard and learned from the root workspace, the cache
	// server or a local copy.
	CoreIdentitySourceLegacy CoreIdentitySource = "Legacy"
	// CoreIdentitySourceKey marks an identity derived from the core
	// identities key (--core-identities-key-file).
	CoreIdentitySourceKey CoreIdentitySource = "Key"
	// CoreIdentitySourceDevFile marks an identity that was bootstrapped
	// from --root-identities-file (development only).
	CoreIdentitySourceDevFile CoreIdentitySource = "DevFile"
)

const (
	// CoreIdentitySetConsistent is True when every identity in this set
	// matches what the root workspace and the other shards use. It is False
	// with reason CoreIdentitySetReasonMismatch when a shard disagrees, and
	// Unknown while no other shard is visible to compare with.
	CoreIdentitySetConsistent conditionsv1alpha1.ConditionType = "Consistent"

	// CoreIdentitySetKeyChanged is True when the mounted core identities
	// key (observedKeyID) differs from the active key (keyID). The shard
	// keeps using its current identities and does not derive identities for
	// new core APIs until the previous key is restored.
	CoreIdentitySetKeyChanged conditionsv1alpha1.ConditionType = "KeyChanged"
)

const (
	// CoreIdentitySetReasonMismatch is the reason for Consistent=False
	// when another shard uses a different identity for a core export.
	CoreIdentitySetReasonMismatch = "Mismatch"
	// CoreIdentitySetReasonNoPeers is the reason for Consistent=Unknown
	// when no other shard's identities are visible yet.
	CoreIdentitySetReasonNoPeers = "NoPeers"
	// CoreIdentitySetReasonUnresolved is the reason for Consistent=Unknown
	// while the shard has not yet resolved all of its core identities.
	CoreIdentitySetReasonUnresolved = "Unresolved"
	// CoreIdentitySetReasonNoKey is the reason for KeyChanged=False when
	// no key has ever been recorded.
	CoreIdentitySetReasonNoKey = "NoKey"
	// CoreIdentitySetReasonKeyChanged is the reason for KeyChanged=True
	// when the mounted key is not the active key.
	CoreIdentitySetReasonKeyChanged = "KeyChanged"
	// CoreIdentitySetReasonKeyRemoved is the reason for KeyChanged=True
	// when a key was recorded but none is mounted anymore.
	CoreIdentitySetReasonKeyRemoved = "KeyRemoved"
)

func (in *CoreIdentitySet) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *CoreIdentitySet) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &CoreIdentitySet{}
var _ conditions.Setter = &CoreIdentitySet{}

// CoreIdentitySetList is a list of CoreIdentitySet resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CoreIdentitySetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []CoreIdentitySet `json:"items"`
}
