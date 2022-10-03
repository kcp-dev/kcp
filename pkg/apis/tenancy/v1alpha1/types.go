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

package v1alpha1

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// RootCluster is the root of ClusterWorkspace based logical clusters.
var RootCluster = logicalcluster.New("root")

// RootShard holds a name of the root shard.
var RootShard = "root"

// ClusterWorkspaceReservedNames defines the set of names that may not be used
// on user-supplied ClusterWorkspaces.
// TODO(hasheddan): tie this definition of reserved names to the patches used to
// apply the same restrictions to the OpenAPISchema.
func ClusterWorkspaceReservedNames() []string {
	return []string{
		"root",
		"system",
	}
}

// ClusterWorkspace defines a Kubernetes-cluster-like endpoint that holds a default set
// of resources and exhibits standard Kubernetes API semantics of CRUD operations. It represents
// the full life-cycle of the persisted data in this workspace in a KCP installation.
//
// ClusterWorkspace is a concrete type that implements a workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase (e.g. Scheduling, Initializing, Ready)"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type.name`,description="Type of the workspace"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.baseURL`,description="URL to access the workspace"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ClusterWorkspace struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceStatus `json:"status,omitempty"`
}

func (in *ClusterWorkspace) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ClusterWorkspace) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ClusterWorkspace{}
var _ conditions.Setter = &ClusterWorkspace{}

// ClusterWorkspaceSpec holds the desired state of the ClusterWorkspace.
type ClusterWorkspaceSpec struct {
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// type defines properties of the workspace both on creation (e.g. initial
	// resources and initially installed APIs) and during runtime (e.g. permissions).
	// If no type is provided, the default type for the workspace in which this workspace
	// is nesting will be used.
	//
	// The type is a reference to a ClusterWorkspaceType in the listed workspace, but
	// lower-cased. The ClusterWorkspaceType existence is validated at admission during
	// creation. The type is immutable after creation. The use of a type is gated via
	// the RBAC clusterworkspacetypes/use resource permission.
	//
	// +optional
	Type ClusterWorkspaceTypeReference `json:"type,omitempty"`

	// shard constraints onto which shards this cluster workspace can be scheduled to.
	// if the constraint is not fulfilled by the current location stored in the status,
	// movement will be attempted.
	//
	// Either shard name or shard selector must be specified.
	//
	// If the no shard constraints are specified, an arbitrary shard is chosen.
	//
	// +optional
	Shard *ShardConstraints `json:"shard,omitempty"`
}

type ShardConstraints struct {
	// name is the name of ClusterWorkspaceShard.
	//
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name,omitempty"`

	// selector is a label selector that filters shard scheduling targets.
	//
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// ClusterWorkspaceTypeReference is a globally unique, fully qualified reference to a
// cluster workspace type.
type ClusterWorkspaceTypeReference struct {
	// name is the name of the ClusterWorkspaceType
	//
	// +required
	// +kubebuilder:validation:Required
	Name ClusterWorkspaceTypeName `json:"name"`

	// path is an absolute reference to the workspace that owns this type, e.g. root:org:ws.
	//
	// +optional
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`
}

// ClusterWorkspaceTypeName is a name of a ClusterWorkspaceType
//
// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?`
type ClusterWorkspaceTypeName string

func (r ClusterWorkspaceTypeReference) String() string {
	return fmt.Sprintf("%s:%s", r.Path, r.Name)
}

func (r ClusterWorkspaceTypeReference) Equal(other ClusterWorkspaceTypeReference) bool {
	return r.Name == other.Name && r.Path == other.Path
}

// ClusterWorkspaceTypeReservedNames defines the set of names that may not be
// used on user-supplied ClusterWorkspaceTypes.
// TODO(hasheddan): tie this definition of reserved names to the patches used to
// apply the same restrictions to the OpenAPISchema.
func ClusterWorkspaceTypeReservedNames() []string {
	return []string{
		"any",
		"system",
	}
}

// ClusterWorkspaceType specifies behaviour of workspaces of this type.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ClusterWorkspaceType struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceTypeSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceTypeStatus `json:"status,omitempty"`
}

type ClusterWorkspaceTypeSpec struct {
	// initializer determines if this ClusterWorkspaceType has an associated initializing
	// controller. These controllers are used to add functionality to a ClusterWorkspace;
	// all controllers must finish their work before the ClusterWorkspace becomes ready
	// for use.
	//
	// One initializing controller is supported per ClusterWorkspaceType; the identifier
	// for this initializer will be a colon-delimited string using the workspace in which
	// the ClusterWorkspaceType is defined, and the type's name. For example, if a
	// ClusterWorkspaceType `example` is created in the `root:org` workspace, the implicit
	// initializer name is `root:org:Example`.
	//
	// +optional
	Initializer bool `json:"initializer,omitempty"`

	// extend is a list of other ClusterWorkspaceTypes whose initializers and limitAllowedChildren
	// and limitAllowedParents this ClusterWorkspaceType is inheriting. By (transitively) extending
	// another ClusterWorkspaceType, this ClusterWorkspaceType will be considered as that
	// other type in evaluation of limitAllowedChildren and limitAllowedParents constraints.
	//
	// A dependency cycle stop this ClusterWorkspaceType from being admitted as the type
	// of a ClusterWorkspace.
	//
	// A non-existing dependency stop this ClusterWorkspaceType from being admitted as the type
	// of a ClusterWorkspace.
	//
	// +optional
	Extend ClusterWorkspaceTypeExtension `json:"extend,omitempty"`

	// additionalWorkspaceLabels are a set of labels that will be added to a
	// ClusterWorkspace on creation.
	//
	// +optional
	AdditionalWorkspaceLabels map[string]string `json:"additionalWorkspaceLabels,omitempty"`

	// defaultChildWorkspaceType is the ClusterWorkspaceType that will be used
	// by default if another, nested ClusterWorkspace is created in a workspace
	// of this type. When this field is unset, the user must specify a type when
	// creating nested workspaces. Extending another ClusterWorkspaceType does
	// not inherit its defaultChildWorkspaceType.
	//
	// +optional
	DefaultChildWorkspaceType *ClusterWorkspaceTypeReference `json:"defaultChildWorkspaceType,omitempty"`

	// limitAllowedChildren specifies constraints for sub-workspaces created in workspaces
	// of this type. These are in addition to child constraints of types this one extends.
	//
	// +optional
	LimitAllowedChildren *ClusterWorkspaceTypeSelector `json:"limitAllowedChildren,omitempty"`

	// limitAllowedParents specifies constraints for the parent workspace that workspaces
	// of this type are created in. These are in addition to parent constraints of types this one
	// extends.
	//
	// +optional
	LimitAllowedParents *ClusterWorkspaceTypeSelector `json:"limitAllowedParents,omitempty"`

	// defaultAPIBindings are the APIs to bind during initialization of workspaces created from this type.
	// The APIBinding names will be generated dynamically.
	//
	// +optional
	// +listType=map
	// +listMapKey=path
	// +listMapKey=exportName
	DefaultAPIBindings []APIExportReference `json:"defaultAPIBindings,omitempty"`
}

// APIExportReference provides the fields necessary to resolve an APIExport.
type APIExportReference struct {
	// path is the fully-qualified path to the workspace containing the APIExport.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern:="^root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Path string `json:"path"`

	// exportName is the name of the APIExport.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kube:validation:MinLength=1
	ExportName string `json:"exportName"`
}

// ClusterWorkspaceTypeSelector describes a set of types.
type ClusterWorkspaceTypeSelector struct {
	// none means that no type matches.
	//
	// +kuberbuilders:Enum=true
	None bool `json:"none,omitempty"`

	// types is a list of ClusterWorkspaceTypes that match. A workspace type extending
	// another workspace type automatically is considered as that extended type as well
	// (even transitively).
	//
	// An empty list matches all types.
	//
	// +optional
	// +kubebuilder:validation:MinItems=1
	Types []ClusterWorkspaceTypeReference `json:"types,omitempty"`
}

// ClusterWorkspaceTypeExtension defines how other ClusterWorkspaceTypes are
// composed together to add functionality to the owning ClusterWorkspaceType.
type ClusterWorkspaceTypeExtension struct {
	// with are ClusterWorkspaceTypes whose initializers are added to the list
	// for the owning type, and for whom the owning type becomes an alias, as long
	// as all of their required types are not mentioned in without.
	//
	// +optional
	With []ClusterWorkspaceTypeReference `json:"with,omitempty"`
}

// These are valid conditions of ClusterWorkspaceType.
const (
	ClusterWorkspaceTypeVirtualWorkspaceURLsReady conditionsv1alpha1.ConditionType = "VirtualWorkspaceURLsReady"
	ErrorGeneratingURLsReason                                                      = "ErrorGeneratingURLs"
)

// ClusterWorkspaceTypeStatus defines the observed state of ClusterWorkspaceType.
type ClusterWorkspaceTypeStatus struct {
	// conditions is a list of conditions that apply to the APIExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// virtualWorkspaces contains all APIExport virtual workspace URLs.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

type VirtualWorkspace struct {
	// url is a ClusterWorkspaceType initialization virtual workspace URL.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`
}

func (in *ClusterWorkspaceType) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

func (in *ClusterWorkspaceType) SetConditions(conditions conditionsv1alpha1.Conditions) {
	in.Status.Conditions = conditions
}

// ObjectName converts the proper name of a type that users interact with to the
// metadata.name of the ClusterWorkspaceType object.
func ObjectName(typeName ClusterWorkspaceTypeName) string {
	return string(typeName)
}

// TypeName converts the metadata.name of a ClusterWorkspaceType to the proper
// name of a type, as users interact with it.
func TypeName(objectName string) ClusterWorkspaceTypeName {
	return ClusterWorkspaceTypeName(objectName)
}

// ReferenceFor returns a reference to the type.
func ReferenceFor(cwt *ClusterWorkspaceType) ClusterWorkspaceTypeReference {
	return ClusterWorkspaceTypeReference{
		Name: TypeName(cwt.Name),
		Path: logicalcluster.From(cwt).String(),
	}
}

// ClusterWorkspaceTypeList is a list of cluster workspace types
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspaceType `json:"items"`
}

// ClusterWorkspaceInitializer is a unique string corresponding to a cluster workspace
// initialization controller for the given type of workspaces.
//
// +kubebuilder:validation:Pattern:="^(root(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*(:[a-z][a-z0-9]([-a-z0-9]*[a-z0-9])?))|(system:.+)$"
type ClusterWorkspaceInitializer string

// ClusterWorkspaceAPIBindingsInitializer is a special-case initializer that waits for APIBindings defined
// on a ClusterWorkspaceType to be created.
const ClusterWorkspaceAPIBindingsInitializer ClusterWorkspaceInitializer = "system:apibindings"

// ClusterWorkspacePhaseType is the type of the current phase of the workspace
type ClusterWorkspacePhaseType string

const (
	ClusterWorkspacePhaseScheduling   ClusterWorkspacePhaseType = "Scheduling"
	ClusterWorkspacePhaseInitializing ClusterWorkspacePhaseType = "Initializing"
	ClusterWorkspacePhaseReady        ClusterWorkspacePhaseType = "Ready"
)

const ExperimentalClusterWorkspaceOwnerAnnotationKey string = "experimental.tenancy.kcp.dev/owner"

// ClusterWorkspaceStatus communicates the observed state of the ClusterWorkspace.
type ClusterWorkspaceStatus struct {
	// Phase of the workspace  (Scheduling / Initializing / Ready)
	Phase ClusterWorkspacePhaseType `json:"phase,omitempty"`

	// Current processing state of the ClusterWorkspace.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// Base URL where this ClusterWorkspace can be targeted.
	// This will generally be of the form: https://<workspace shard server>/cluster/<workspace name>.
	// But a workspace could also be targetable by a unique hostname in the future.
	//
	// +kubebuilder:validation:Pattern:https://[^/].*
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Contains workspace placement information.
	//
	// +optional
	Location ClusterWorkspaceLocation `json:"location,omitempty"`

	// initializers are set on creation by the system and must be cleared
	// by a controller before the workspace can be used. The workspace will
	// stay in the phase "Initializing" state until all initializers are cleared.
	//
	// A cluster workspace in "Initializing" state are gated via the RBAC
	// clusterworkspaces/initialize resource permission.
	//
	// +optional
	Initializers []ClusterWorkspaceInitializer `json:"initializers,omitempty"`
}

// These are valid conditions of workspace.
const (
	// WorkspaceScheduled represents status of the scheduling process for this workspace.
	WorkspaceScheduled conditionsv1alpha1.ConditionType = "WorkspaceScheduled"
	// WorkspaceReasonUnschedulable reason in WorkspaceScheduled WorkspaceCondition means that the scheduler
	// can't schedule the workspace right now, for example due to insufficient resources in the cluster.
	WorkspaceReasonUnschedulable = "Unschedulable"
	// WorkspaceReasonReasonUnknown reason in WorkspaceScheduled means that scheduler has failed for
	// some unexpected reason.
	WorkspaceReasonReasonUnknown = "Unknown"
	// WorkspaceReasonUnreschedulable reason in WorkspaceScheduled WorkspaceCondition means that the scheduler
	// can't reschedule the workspace right now, for example because it not in Scheduling phase anymore and
	// movement is not possible.
	WorkspaceReasonUnreschedulable = "Unreschedulable"

	// WorkspaceShardValid represents status of the connection process for this cluster workspace.
	WorkspaceShardValid conditionsv1alpha1.ConditionType = "WorkspaceShardValid"
	// WorkspaceShardValidReasonShardNotFound reason in WorkspaceShardValid condition means that the
	// referenced ClusterWorkspaceShard object got deleted.
	WorkspaceShardValidReasonShardNotFound = "ShardNotFound"

	// WorkspaceDeletionContentSuccess represents the status that all resources in the workspace is deleting
	WorkspaceDeletionContentSuccess conditionsv1alpha1.ConditionType = "WorkspaceDeletionContentSuccess"

	// WorkspaceContentDeleted represents the status that all resources in the workspace is deleted.
	WorkspaceContentDeleted conditionsv1alpha1.ConditionType = "WorkspaceContentDeleted"

	// WorkspaceInitialized represents the status that initialization has finished.
	WorkspaceInitialized conditionsv1alpha1.ConditionType = "WorkspaceInitialized"
	// WorkspaceInitializedInitializerExists reason in WorkspaceInitialized condition means that there is at least
	// one initializer still left.
	WorkspaceInitializedInitializerExists = "InitializerExists"

	// WorkspaceAPIBindingsInitialized represents the status of the initial APIBindings for the workspace.
	WorkspaceAPIBindingsInitialized conditionsv1alpha1.ConditionType = "APIBindingsInitialized"
	// WorkspaceInitializedWaitingOnAPIBindings is a reason for the APIBindingsInitialized condition that indicates
	// at least one APIBinding is not ready.
	WorkspaceInitializedWaitingOnAPIBindings = "WaitingOnAPIBindings"
	// WorkspaceInitializedClusterWorkspaceTypeInvalid is a reason for the APIBindingsInitialized
	// condition that indicates something is invalid with the ClusterWorkspaceType (e.g. a cycle trying
	// to resolve all the transitive types).
	WorkspaceInitializedClusterWorkspaceTypeInvalid = "ClusterWorkspaceTypeInvalid"
	// WorkspaceInitializedAPIBindingErrors is a reason for the APIBindingsInitialized condition that indicates there
	// were errors trying to initialize APIBindings for the workspace.
	WorkspaceInitializedAPIBindingErrors = "APIBindingErrors"
)

// ClusterWorkspaceLocation specifies workspace placement information, including current, desired (target), and
// historical information.
type ClusterWorkspaceLocation struct {
	// Current workspace placement (shard).
	//
	// +optional
	Current string `json:"current,omitempty"`

	// Target workspace placement (shard).
	//
	// +optional
	// +kubebuilder:validation:Enum=""
	Target string `json:"target,omitempty"`
}

// ClusterWorkspaceList is a list of ClusterWorkspace resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspace `json:"items"`
}

// ClusterWorkspaceShard describes a Shard (== KCP instance) on which a number of
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
type ClusterWorkspaceShard struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterWorkspaceShardSpec `json:"spec,omitempty"`

	// +optional
	Status ClusterWorkspaceShardStatus `json:"status,omitempty"`
}

func (in *ClusterWorkspaceShard) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *ClusterWorkspaceShard) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &ClusterWorkspaceShard{}
var _ conditions.Setter = &ClusterWorkspaceShard{}

// ClusterWorkspaceShardSpec holds the desired state of the ClusterWorkspaceShard.
type ClusterWorkspaceShardSpec struct {
	// baseURL is the address of the KCP shard for direct connections, e.g. by some
	// front-proxy doing the fan-out to the shards.
	//
	// This will be defaulted to the shard's external address if not specified. Note that this
	// is only sensible in single-shards setups.
	//
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

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
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Required
	// +required
	ExternalURL string `json:"externalURL"`

	// virtualWorkspaceURL is the address of the virtual workspace server associated with this shard.
	// It can be a direct address, an address of a front-proxy or even an address of an LB.
	// As of today this address is assigned to APIExports.
	//
	// This will be defaulted to the shard's base address if not specified.
	//
	// +kubebuilder:validation:Format=uri
	// +kubebuilder:validation:MinLength=1
	// +optional
	VirtualWorkspaceURL string `json:"virtualWorkspaceURL,omitempty"`
}

// ClusterWorkspaceShardStatus communicates the observed state of the ClusterWorkspaceShard.
type ClusterWorkspaceShardStatus struct {
	// Set of integer resources that workspaces can be scheduled into
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// Current processing state of the ClusterWorkspaceShard.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ClusterWorkspaceShardList is a list of workspace shards
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ClusterWorkspaceShardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ClusterWorkspaceShard `json:"items"`
}

const (
	// ClusterWorkspacePhaseLabel holds the ClusterWorkspace.Status.Phase value, and is enforced to match
	// by a mutating admission webhook.
	ClusterWorkspacePhaseLabel = "internal.kcp.dev/phase"
	// ClusterWorkspaceInitializerLabelPrefix is the prefix for labels which match ClusterWorkspace.Status.Initializers,
	// and the set of labels with this prefix is enforced to match the set of initializers by a mutating admission
	// webhook.
	ClusterWorkspaceInitializerLabelPrefix = "initializer.internal.kcp.dev/"
)

const (
	// RootWorkspaceTypeName is a reference to the root logical cluster, which has no cluster workspace type
	RootWorkspaceTypeName = ClusterWorkspaceTypeName("root")
)

var (
	// RootWorkspaceTypeReference is a reference to the root logical cluster, which has no cluster workspace type
	RootWorkspaceTypeReference = ClusterWorkspaceTypeReference{
		Name: RootWorkspaceTypeName,
		Path: RootCluster.String(),
	}

	// RootWorkspaceType is the implicit type of the root logical cluster.
	RootWorkspaceType = &ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: ObjectName(RootWorkspaceTypeReference.Name),
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: RootWorkspaceTypeReference.Path,
			},
		},
		Spec: ClusterWorkspaceTypeSpec{
			LimitAllowedParents: &ClusterWorkspaceTypeSelector{
				None: true,
			},
		},
	}
)
