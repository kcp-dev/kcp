/*
Copyright 2022 The kcp Authors.

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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// RootShard holds a name of the root shard.
var RootShard = "root"

// ShardRepresentationAnnotationKey marks a Shard object as a read-only
// representation mirrored from the shard-owned authoritative object living in
// the shard's local system:shard logical cluster. It is mostly read-only, with
// only exception is a small allow-list of operational annotations (like
// ShardUnschedulableAnnotationKey) that admins may set on the representation;
// those are synced back to the authoritative object instead of being overwritten.
const ShardRepresentationAnnotationKey = "core.kcp.io/shard-representation"

// ShardUnschedulableAnnotationKey marks a single shard as unschedulable: the
// workspace scheduler will not place new workspaces on it (cordoning). It
// only affects the shard whose Shard object carries it.
// Admins set it on that shard's representation in the root workspace; the
// shard mirror syncs it back to the shard-owned authoritative object in the
// owning shard's system:shard logical cluster, from where cache replication
// makes it visible to the workspace schedulers running on every shard.
const ShardUnschedulableAnnotationKey = "experimental.core.kcp.io/unschedulable"

// ShardSchedulable is a condition on the Shard object reflecting the shard's
// scheduling state, similar to a Kubernetes node: True when new workspaces
// may be scheduled onto the shard, False with reason ShardReasonCordoned when
// the shard observed the unschedulable annotation on its authoritative
// object. It is set by the owning shard itself, so seeing it change on the
// representation in the root workspace acknowledges that the shard received
// and applied the cordon/uncordon signal.
const ShardSchedulable v1alpha1.ConditionType = "Schedulable"

// ShardReasonCordoned is the reason for ShardSchedulable=False when the
// owning shard observed the unschedulable annotation on its Shard object.
const ShardReasonCordoned = "Cordoned"

// Shard describes a kcp instance on which a number of logical clusters will live
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.metadata.labels['region']`,description="The region this workspace is in"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.baseURL`,description="Type URL to directly connect to the shard"
// +kubebuilder:printcolumn:name="External URL",type=string,JSONPath=`.spec.externalURL`,description="The URL exposed in logical clusters created on that shard"
// +kubebuilder:printcolumn:name="Schedulable",type=string,JSONPath=`.status.conditions[?(@.type=="Schedulable")].status`,description="Whether new workspaces are scheduled onto this shard"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Shard struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ShardSpec `json:"spec,omitempty"`

	// +optional
	Status ShardStatus `json:"status,omitempty"`
}

func (in *Shard) SetConditions(c v1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *Shard) GetConditions() v1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &Shard{}
var _ conditions.Setter = &Shard{}

// ShardSpec holds the desired state of the Shard.
type ShardSpec struct {
	// baseURL is the address of the kcp shard for direct connections, e.g. by some
	// front-proxy doing the fan-out to the shards.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	BaseURL string `json:"baseURL"`

	// externalURL is the externally visible address presented to users in Workspace URLs.
	// Changing this will break all existing logical clusters on that shard, i.e. existing
	// kubeconfigs of clients will be invalid. Hence, when changing this value, the old
	// URL used by clients must keep working.
	//
	// The external address will not be unique if a front-proxy does a fan-out to
	// shards, but all logical cluster clients will talk to the front-proxy. In that case,
	// put the address of the front-proxy here.
	//
	// Note that movement of shards is only possible (in the future) between shards
	// that share a common external URL.
	//
	// This will be defaulted to the value of the baseURL.
	//
	// +optional
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	ExternalURL string `json:"externalURL,omitempty"`

	// virtualWorkspaceURL is the address of the virtual workspace apiserver associated with this shard.
	// It can be a direct address, an address of a front-proxy or even an address of an LB.
	// As of today this address is assigned to APIExports.
	//
	// This will be defaulted to the value of the baseURL.
	//
	// +optional
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	VirtualWorkspaceURL string `json:"virtualWorkspaceURL,omitempty"`
}

// ShardStatus communicates the observed state of the Shard.
type ShardStatus struct {
	// Set of integer resources that logical clusters can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the Shard.
	// +optional
	Conditions v1alpha1.Conditions `json:"conditions,omitempty"`
}

// ShardList is a list of shard instances
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ShardList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []Shard `json:"items"`
}
