package v1alpha1

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// LogicalCluster describes the current workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.metadata.labels['tenancy\.kcp\.dev/phase']`,description="The current phase (e.g. Scheduling, Initializing, Ready, Deleting)"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.URL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type LogicalCluster struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec LogicalClusterSpec `json:"spec,omitempty"`
	// +optional
	Status LogicalClusterStatus `json:"status,omitempty"`
}

const (
	// LogicalClusterName is the name of the LogicalCluster singleton.
	LogicalClusterName = "this"

	// LogicalClusterFinalizer attached to new ClusterWorkspace (in phase LogicalClusterPhaseScheduling) resources so that we can control
	// deletion of LogicalCluster resources
	LogicalClusterFinalizer = "core.kcp.dev/logicalcluster"

	// LogicalClusterTypeAnnotationKey is the annotation key used to indicate
	// the type of the workspace on the LogicalCluster object. Its format is "root:ws:name".
	LogicalClusterTypeAnnotationKey = "internal.tenancy.kcp.dev/type"
)

// LogicalClusterPhaseType is the type of the current phase of the logical cluster.
//
// +kubebuilder:validation:Enum=Scheduling;Initializing;Ready
type LogicalClusterPhaseType string

const (
	LogicalClusterPhaseScheduling   LogicalClusterPhaseType = "Scheduling"
	LogicalClusterPhaseInitializing LogicalClusterPhaseType = "Initializing"
	LogicalClusterPhaseReady        LogicalClusterPhaseType = "Ready"
)

// LogicalClusterInitializer is a unique string corresponding to a logical cluster
// initialization controller for the given type of workspaces.
//
// +kubebuilder:validation:Pattern:="^([a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*(:[a-z0-9][a-z0-9]([-a-z0-9]*[a-z0-9])?))|(system:.+)$"
type LogicalClusterInitializer string

// LogicalClusterSpec is the specification of the LogicalCluster resource.
type LogicalClusterSpec struct {
	// DirectlyDeletable indicates that this workspace can be directly deleted by the user
	// from within the workspace.
	//
	// +optional
	// +kubebuilder:default=false
	DirectlyDeletable bool `json:"directlyDeletable,omitempty"`

	// owner is a reference to a resource controlling the life-cycle of this workspace.
	// On deletion of the LogicalCluster, the finalizer core.kcp.dev/logicalcluster is
	// removed from the owner.
	//
	// When this object is deleted, but the owner is not deleted, the owner is deleted
	// too.
	//
	// +optional
	Owner *LogicalClusterOwner `json:"owner,omitempty"`

	// initializers are set on creation by the system and copied to status when
	// initialization starts.
	//
	// +optional
	Initializers []LogicalClusterInitializer `json:"initializers,omitempty"`
}

// LogicalClusterOwner is a reference to a resource controlling the life-cycle of a LogicalCluster.
type LogicalClusterOwner struct {
	// apiVersion is the group and API version of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^([^/]+/)?[^/]+$`
	APIVersion string `json:"apiVersion"`

	// resource is API resource to access the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// name is the name of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// namespace is the optional namespace of the owner.
	//
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// cluster is the logical cluster in which the owner is located.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Cluster string `json:"cluster"`

	// UID is the UID of the owner.
	//
	// +required
	// +kubebuilder:validation:Required
	UID types.UID `json:"uid"`
}

// LogicalClusterStatus communicates the observed state of the Workspace.
type LogicalClusterStatus struct {
	// url is the address under which the Kubernetes-cluster-like endpoint
	// can be found. This URL can be used to access the workspace with standard Kubernetes
	// client libraries and command line tools.
	//
	// +kubebuilder:format:uri
	URL string `json:"URL,omitempty"`

	// Phase of the workspace (Initializing, Ready).
	//
	// +kubebuilder:default=Scheduling
	Phase LogicalClusterPhaseType `json:"phase,omitempty"`

	// Current processing state of the LogicalCluster.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// initializers are set on creation by the system and must be cleared
	// by a controller before the workspace can be used. The LogicalCluster will
	// stay in the phase "Initializing" state until all initializers are cleared.
	//
	// +optional
	Initializers []LogicalClusterInitializer `json:"initializers,omitempty"`
}

func (in *LogicalCluster) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *LogicalCluster) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &LogicalCluster{}
var _ conditions.Setter = &LogicalCluster{}

// LogicalClusterList is a list of LogicalCluster
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LogicalClusterList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []LogicalCluster `json:"items"`
}
