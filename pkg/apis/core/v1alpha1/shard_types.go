package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// RootShard holds a name of the root shard.
var RootShard = "root"

// Shard describes a Shard (== KCP instance) on which a number of
// workspaces will live
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.spec.baseURL`,description="Type URL to directly connect to the shard"
// +kubebuilder:printcolumn:name="External URL",type=string,JSONPath=`.spec.externalURL`,description="The URL exposed in workspaces created on that shard"
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
	// baseURL is the address of the KCP shard for direct connections, e.g. by some
	// front-proxy doing the fan-out to the shards.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	BaseURL string `json:"baseURL"`

	// externalURL is the externally visible address presented to users in Workspace URLs.
	// Changing this will break all existing workspaces on that shard, i.e. existing
	// kubeconfigs of clients will be invalid. Hence, when changing this value, the old
	// URL used by clients must keep working.
	//
	// The external address will not be unique if a front-proxy does a fan-out to
	// shards, but all workspace client will talk to the front-proxy. In that case,
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

	// virtualWorkspaceURL is the address of the virtual workspace server associated with this shard.
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
	// Set of integer resources that workspaces can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the Shard.
	// +optional
	Conditions v1alpha1.Conditions `json:"conditions,omitempty"`
}

// ShardList is a list of workspace shards
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ShardList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []Shard `json:"items"`
}
