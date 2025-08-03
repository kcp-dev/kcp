/*
Copyright 2025 The KCP Authors.

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
	"k8s.io/apimachinery/pkg/runtime"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
)

// SyncTarget represents a physical cluster that can accept workloads from KCP.
// It defines the cluster's capabilities, supported resource types, and connection
// information for the syncer component.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=st
// +kubebuilder:printcolumn:name="Location",type=string,JSONPath=`.metadata.labels['location']`,description="Location label of the SyncTarget"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Syncer",type=string,JSONPath=`.status.syncerIdentifier`,description="Syncer that handles this target"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SyncTarget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the SyncTarget
	Spec SyncTargetSpec `json:"spec,omitempty"`

	// Status represents the current state of the SyncTarget
	// +optional
	Status SyncTargetStatus `json:"status,omitempty"`
}

// SyncTargetSpec defines the desired state of the SyncTarget per PRD requirements
type SyncTargetSpec struct {
	// WorkloadCluster defines workspace-aware cluster connection
	WorkloadCluster WorkloadClusterReference `json:"workloadCluster"`

	// SupportedAPIExports lists the APIExports that this cluster can consume
	// +optional
	SupportedAPIExports []APIExportReference `json:"supportedAPIExports,omitempty"`

	// Capabilities define cluster capabilities for workload placement
	// +optional
	Capabilities []CapabilitySpec `json:"capabilities,omitempty"`

	// Workspaces define the workspace scope for this SyncTarget
	// +optional
	Workspaces []WorkspaceReference `json:"workspaces,omitempty"`

	// Unschedulable controls cluster scheduling. When true, new workloads will not
	// be scheduled to this cluster, but existing workloads will not be affected.
	// +optional
	Unschedulable bool `json:"unschedulable,omitempty"`

	// EvictAfter specifies the time after which workloads should be evicted from
	// this cluster if the cluster becomes unreachable.
	// +optional
	EvictAfter *metav1.Duration `json:"evictAfter,omitempty"`

	// Cells define the cluster's organization into cells for advanced placement.
	// +optional
	Cells map[string]string `json:"cells,omitempty"`
}

// WorkloadClusterReference defines workspace-aware cluster connection
type WorkloadClusterReference struct {
	// Name identifies the workload cluster
	Name string `json:"name"`

	// Endpoint specifies the cluster endpoint
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Credentials reference for cluster authentication
	// +optional
	Credentials *ClusterCredentials `json:"credentials,omitempty"`
}

// ClusterCredentials defines cluster authentication credentials
type ClusterCredentials struct {
	// SecretRef references a secret containing cluster credentials
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// ServiceAccountRef references a service account for cluster access
	// +optional
	ServiceAccountRef *ServiceAccountReference `json:"serviceAccountRef,omitempty"`
}

// SecretReference references a secret
type SecretReference struct {
	// Name of the secret
	Name string `json:"name"`

	// Namespace of the secret
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ServiceAccountReference references a service account
type ServiceAccountReference struct {
	// Name of the service account
	Name string `json:"name"`

	// Namespace of the service account
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// APIExportReference references an APIExport
type APIExportReference struct {
	// Export identifies the APIExport
	Export string `json:"export"`

	// Workspace specifies the workspace containing the APIExport
	// +optional
	Workspace string `json:"workspace,omitempty"`
}

// CapabilitySpec defines a cluster capability
type CapabilitySpec struct {
	// Type of capability (e.g., "storage", "compute", "networking")
	Type string `json:"type"`

	// Available indicates if the capability is available
	Available bool `json:"available"`

	// Metadata provides additional capability information
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`
}

// WorkspaceReference references a workspace
type WorkspaceReference struct {
	// Name of the workspace
	Name string `json:"name"`

	// Path to the workspace
	// +optional
	Path string `json:"path,omitempty"`
}

// SyncTargetStatus represents the observed state of the SyncTarget per PRD requirements
type SyncTargetStatus struct {
	// Conditions represent the latest available observations of the SyncTarget's current state.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// SyncedAPIExports tracks the status of APIExport synchronization
	// +optional
	SyncedAPIExports []APIExportStatus `json:"syncedAPIExports,omitempty"`

	// ResourceVersions track resource versions for synchronization
	// +optional
	ResourceVersions map[string]string `json:"resourceVersions,omitempty"`

	// HeartbeatTime tracks the last syncer heartbeat
	// +optional
	HeartbeatTime metav1.Time `json:"heartbeatTime,omitempty"`

	// AllocatableCapacity represents the resources allocatable for workloads
	// +optional
	AllocatableCapacity ResourceCapacityStatus `json:"allocatableCapacity,omitempty"`

	// Capacity represents the total resources available on the cluster
	// +optional
	Capacity ResourceCapacityStatus `json:"capacity,omitempty"`

	// SyncerIdentifier identifies the syncer process handling this cluster
	// +optional
	SyncerIdentifier string `json:"syncerIdentifier,omitempty"`

	// LastSyncerHeartbeatTime is the last time the syncer sent a heartbeat
	// +optional
	LastSyncerHeartbeatTime *metav1.Time `json:"lastSyncerHeartbeatTime,omitempty"`

	// VirtualWorkspaces contains a list of URLs where the syncer can access
	// virtual workspaces for this cluster.
	// +optional
	VirtualWorkspaces []VirtualWorkspace `json:"virtualWorkspaces,omitempty"`
}

// APIExportStatus tracks APIExport sync status
type APIExportStatus struct {
	// Export identifies the APIExport
	Export string `json:"export"`

	// Synced indicates if the APIExport is synced
	Synced bool `json:"synced"`

	// LastSyncTime records the last sync time
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Error records any sync errors
	// +optional
	Error string `json:"error,omitempty"`
}

// ResourceCapacityStatus represents resource capacity information
type ResourceCapacityStatus struct {
	// CPU capacity in millicores
	// +optional
	CPU *int64 `json:"cpu,omitempty"`

	// Memory capacity in bytes
	// +optional
	Memory *int64 `json:"memory,omitempty"`

	// Storage capacity in bytes
	// +optional
	Storage *int64 `json:"storage,omitempty"`

	// Pods represents maximum pod capacity
	// +optional
	Pods *int64 `json:"pods,omitempty"`
}

// VirtualWorkspace represents a virtual workspace endpoint
type VirtualWorkspace struct {
	// URL is the address where the virtual workspace can be accessed
	URL string `json:"url"`

	// LogicalCluster is the logical cluster this virtual workspace serves
	LogicalCluster string `json:"logicalCluster"`
}

// Placement represents a policy for placing workloads across SyncTargets.
// It defines how resources should be distributed, scheduled, and managed
// across the available clusters with workspace awareness.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=pl
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.placementPolicy.placementStrategy`,description="Placement strategy"
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.source.workspace`,description="Source workspace"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Placement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired placement policy
	Spec PlacementSpec `json:"spec,omitempty"`

	// Status represents the current state of the placement
	// +optional
	Status PlacementStatus `json:"status,omitempty"`
}

// PlacementSpec defines the desired placement policy per PRD requirements
type PlacementSpec struct {
	// Source workspace and workload reference
	Source WorkloadReference `json:"source"`

	// LocationSelector specifies target location selection
	LocationSelector LocationSelector `json:"locationSelector"`

	// Constraints define scheduling constraints
	Constraints PlacementConstraints `json:"constraints"`

	// Strategy defines placement strategy (one-to-any, one-to-many)
	Strategy PlacementStrategy `json:"strategy"`
}

// WorkloadReference references a workload in a specific workspace
type WorkloadReference struct {
	// Workspace containing the workload
	Workspace string `json:"workspace"`

	// Namespace containing the workload (if namespaced)
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the workload
	Name string `json:"name"`

	// Kind of the workload
	Kind string `json:"kind"`

	// APIVersion of the workload
	APIVersion string `json:"apiVersion"`
}

// LocationSelector selects target locations for placement
type LocationSelector struct {
	// Path specifies the location path pattern to match
	// +optional
	Path string `json:"path,omitempty"`

	// LabelSelector selects locations based on labels
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// PlacementConstraints define scheduling constraints
type PlacementConstraints struct {
	// ResourceRequirements specify resource requirements
	// +optional
	ResourceRequirements *ResourceRequirements `json:"resourceRequirements,omitempty"`

	// Affinity specifies affinity constraints
	// +optional
	Affinity *PlacementAffinity `json:"affinity,omitempty"`

	// Tolerations specify tolerations for cluster taints
	// +optional
	Tolerations []PlacementToleration `json:"tolerations,omitempty"`

	// TopologySpreadConstraints defines distribution across topology domains
	// +optional
	TopologySpreadConstraints []TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
}

// ResourceRequirements specify resource requirements for placement
type ResourceRequirements struct {
	// CPU requirements in millicores
	// +optional
	CPU *int64 `json:"cpu,omitempty"`

	// Memory requirements in bytes
	// +optional
	Memory *int64 `json:"memory,omitempty"`

	// Storage requirements in bytes
	// +optional
	Storage *int64 `json:"storage,omitempty"`
}

// PlacementStrategy defines placement strategies
// +kubebuilder:validation:Enum=OneToAny;OneToMany;Spread
type PlacementStrategy string

const (
	// OneToAnyStrategy places workload in a single best-match cluster
	OneToAnyStrategy PlacementStrategy = "OneToAny"

	// OneToManyStrategy replicates workload to all matching clusters
	OneToManyStrategy PlacementStrategy = "OneToMany"

	// SpreadStrategy distributes workload across clusters with constraints
	SpreadStrategy PlacementStrategy = "Spread"
)

// PlacementAffinity defines affinity constraints
type PlacementAffinity struct {
	// ClusterAffinity specifies cluster affinity
	// +optional
	ClusterAffinity *ClusterAffinity `json:"clusterAffinity,omitempty"`

	// ClusterAntiAffinity specifies cluster anti-affinity
	// +optional
	ClusterAntiAffinity *ClusterAntiAffinity `json:"clusterAntiAffinity,omitempty"`
}

// ClusterAffinity defines cluster affinity
type ClusterAffinity struct {
	// RequiredDuringSchedulingIgnoredDuringExecution specifies hard constraints
	// +optional
	RequiredDuringSchedulingIgnoredDuringExecution *ClusterAffinityTerm `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`

	// PreferredDuringSchedulingIgnoredDuringExecution specifies soft constraints
	// +optional
	PreferredDuringSchedulingIgnoredDuringExecution []WeightedClusterAffinityTerm `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

// ClusterAntiAffinity defines cluster anti-affinity
type ClusterAntiAffinity struct {
	// RequiredDuringSchedulingIgnoredDuringExecution specifies hard anti-affinity
	// +optional
	RequiredDuringSchedulingIgnoredDuringExecution *ClusterAffinityTerm `json:"requiredDuringSchedulingIgnoredDuringExecution,omitempty"`

	// PreferredDuringSchedulingIgnoredDuringExecution specifies soft anti-affinity
	// +optional
	PreferredDuringSchedulingIgnoredDuringExecution []WeightedClusterAffinityTerm `json:"preferredDuringSchedulingIgnoredDuringExecution,omitempty"`
}

// ClusterAffinityTerm defines a cluster affinity term
type ClusterAffinityTerm struct {
	// LabelSelector selects clusters based on labels
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// Locations specifies location constraints
	// +optional
	Locations []string `json:"locations,omitempty"`
}

// WeightedClusterAffinityTerm defines a weighted cluster affinity term
type WeightedClusterAffinityTerm struct {
	// Weight associated with the term, range 1-100
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`

	// ClusterAffinityTerm specifies the cluster affinity term
	ClusterAffinityTerm ClusterAffinityTerm `json:"clusterAffinityTerm"`
}

// PlacementToleration defines toleration for cluster taints
type PlacementToleration struct {
	// Key is the taint key that the toleration applies to
	// +optional
	Key string `json:"key,omitempty"`

	// Operator represents the relationship between the key and value
	// +kubebuilder:validation:Enum=Exists;Equal
	// +kubebuilder:default=Equal
	Operator TolerationOperator `json:"operator,omitempty"`

	// Value is the taint value that the toleration matches
	// +optional
	Value string `json:"value,omitempty"`

	// Effect indicates which taint effect this toleration matches
	// +optional
	Effect TaintEffect `json:"effect,omitempty"`

	// TolerationSeconds specifies how long the pod can be bound to a tainted cluster
	// +optional
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

// TolerationOperator defines toleration operators
// +kubebuilder:validation:Enum=Exists;Equal
type TolerationOperator string

const (
	// TolerationOpExists means the toleration matches any taint with the matching key
	TolerationOpExists TolerationOperator = "Exists"

	// TolerationOpEqual means the toleration matches taints with matching key and value
	TolerationOpEqual TolerationOperator = "Equal"
)

// TaintEffect defines taint effects
type TaintEffect string

const (
	// TaintEffectNoSchedule means workloads will not be scheduled to the cluster
	TaintEffectNoSchedule TaintEffect = "NoSchedule"

	// TaintEffectPreferNoSchedule means workloads will prefer not to be scheduled to the cluster
	TaintEffectPreferNoSchedule TaintEffect = "PreferNoSchedule"

	// TaintEffectNoExecute means workloads will not be scheduled and existing ones will be evicted
	TaintEffectNoExecute TaintEffect = "NoExecute"
)

// TopologySpreadConstraint defines workload distribution across topology domains
type TopologySpreadConstraint struct {
	// TopologyKey specifies the topology domain key
	TopologyKey string `json:"topologyKey"`

	// WhenUnsatisfiable specifies behavior when constraint cannot be satisfied
	// +kubebuilder:validation:Enum=DoNotSchedule;ScheduleAnyway
	WhenUnsatisfiable UnsatisfiableConstraintAction `json:"whenUnsatisfiable"`

	// MaxSkew defines the maximum difference in workload distribution
	// +kubebuilder:validation:Minimum=1
	MaxSkew int32 `json:"maxSkew"`

	// LabelSelector selects workloads subject to this constraint
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// UnsatisfiableConstraintAction defines actions for unsatisfiable constraints
// +kubebuilder:validation:Enum=DoNotSchedule;ScheduleAnyway
type UnsatisfiableConstraintAction string

const (
	// DoNotSchedule means don't schedule when constraint cannot be satisfied
	DoNotSchedule UnsatisfiableConstraintAction = "DoNotSchedule"

	// ScheduleAnyway means schedule even when constraint cannot be satisfied
	ScheduleAnyway UnsatisfiableConstraintAction = "ScheduleAnyway"
)

// PlacementStatus represents the observed state of the placement per PRD requirements
type PlacementStatus struct {
	// Conditions represent the latest available observations of the placement's current state
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// PlacementDecisions lists the placement decisions made
	// +optional
	PlacementDecisions []PlacementDecision `json:"placementDecisions,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed placement spec
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// PlacementDecision represents a placement decision
type PlacementDecision struct {
	// Cluster is the name of the selected cluster
	Cluster string `json:"cluster"`

	// Reason explains why this cluster was selected
	// +optional
	Reason string `json:"reason,omitempty"`

	// Weight is the score assigned to this cluster
	// +optional
	Weight int32 `json:"weight,omitempty"`
}

// Location represents a logical location where SyncTargets can be organized.
// Locations provide a hierarchical way to group clusters by geography,
// availability zones, data centers, or other organizational boundaries.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=loc
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`,description="Display name of the location"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.locationType`,description="Type of the location"
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parent`,description="Parent location"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type Location struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the Location
	Spec LocationSpec `json:"spec,omitempty"`

	// Status represents the current state of the Location
	// +optional
	Status LocationStatus `json:"status,omitempty"`
}

// LocationSpec defines the desired state of the Location
type LocationSpec struct {
	// DisplayName is a human-readable name for the location
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Description provides additional information about the location
	// +optional
	Description string `json:"description,omitempty"`

	// LocationType categorizes the location (e.g., region, zone, datacenter)
	// +kubebuilder:validation:Enum=Region;AvailabilityZone;DataCenter;Edge;Cloud;OnPremise
	// +kubebuilder:default=Region
	LocationType LocationType `json:"locationType,omitempty"`

	// Parent specifies the parent location in the hierarchy
	// +optional
	Parent string `json:"parent,omitempty"`

	// Labels provide additional metadata for the location
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// InstanceTypes define the available compute instance types at this location
	// +optional
	InstanceTypes []InstanceType `json:"instanceTypes,omitempty"`

	// AvailabilityZones list the availability zones within this location
	// +optional
	AvailabilityZones []string `json:"availabilityZones,omitempty"`

	// Coordinates specify the geographical coordinates of the location
	// +optional
	Coordinates *GeographicalCoordinates `json:"coordinates,omitempty"`
}

// LocationType defines different types of locations
// +kubebuilder:validation:Enum=Region;AvailabilityZone;DataCenter;Edge;Cloud;OnPremise
type LocationType string

const (
	// RegionLocationType represents a geographical region
	RegionLocationType LocationType = "Region"

	// AvailabilityZoneLocationType represents an availability zone within a region
	AvailabilityZoneLocationType LocationType = "AvailabilityZone"

	// DataCenterLocationType represents a physical data center
	DataCenterLocationType LocationType = "DataCenter"

	// EdgeLocationType represents an edge computing location
	EdgeLocationType LocationType = "Edge"

	// CloudLocationType represents a cloud provider location
	CloudLocationType LocationType = "Cloud"

	// OnPremiseLocationType represents an on-premise location
	OnPremiseLocationType LocationType = "OnPremise"
)

// InstanceType defines a compute instance type available at a location
type InstanceType struct {
	// Name is the identifier for the instance type
	Name string `json:"name"`

	// CPU specifies the CPU capacity in millicores
	CPU int64 `json:"cpu"`

	// Memory specifies the memory capacity in bytes
	Memory int64 `json:"memory"`

	// Storage specifies the storage capacity in bytes
	// +optional
	Storage *int64 `json:"storage,omitempty"`

	// GPU specifies the number of GPU units
	// +optional
	GPU *int64 `json:"gpu,omitempty"`

	// Accelerators define specialized compute accelerators
	// +optional
	Accelerators []Accelerator `json:"accelerators,omitempty"`
}

// Accelerator defines a specialized compute accelerator
type Accelerator struct {
	// Type specifies the accelerator type (e.g., "nvidia.com/gpu", "amd.com/gpu")
	Type string `json:"type"`

	// Count specifies the number of accelerator units
	Count int64 `json:"count"`
}

// GeographicalCoordinates specify location coordinates
type GeographicalCoordinates struct {
	// Latitude specifies the latitude coordinate
	Latitude float64 `json:"latitude"`

	// Longitude specifies the longitude coordinate
	Longitude float64 `json:"longitude"`
}

// LocationStatus represents the observed state of the Location
type LocationStatus struct {
	// Conditions represent the latest available observations of the location's current state
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// AvailableClusters lists the SyncTargets available at this location
	// +optional
	AvailableClusters []string `json:"availableClusters,omitempty"`

	// TotalCapacity represents the total resource capacity across all clusters at this location
	// +optional
	TotalCapacity ResourceCapacityStatus `json:"totalCapacity,omitempty"`

	// AllocatedCapacity represents the currently allocated resources at this location
	// +optional
	AllocatedCapacity ResourceCapacityStatus `json:"allocatedCapacity,omitempty"`

	// ChildLocations lists the child locations under this location
	// +optional
	ChildLocations []string `json:"childLocations,omitempty"`

	// Path represents the full path from root to this location
	// +optional
	Path string `json:"path,omitempty"`
}

// ResourceImport represents a resource that has been imported from a SyncTarget
// into a KCP workspace for management and scheduling decisions.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=ri
// +kubebuilder:printcolumn:name="Source Cluster",type=string,JSONPath=`.spec.source.cluster`,description="Source cluster name"
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=`.spec.source.resource.resource`,description="Resource type"
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.source.resource.namespace`,description="Resource namespace"
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.source.resource.name`,description="Resource name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ResourceImport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the ResourceImport
	Spec ResourceImportSpec `json:"spec,omitempty"`

	// Status represents the current state of the ResourceImport
	// +optional
	Status ResourceImportStatus `json:"status,omitempty"`
}

// ResourceImportSpec defines the desired state of the ResourceImport
type ResourceImportSpec struct {
	// Source specifies the source of the imported resource
	Source ResourceSource `json:"source"`

	// SchemaUpdatePolicy defines how schema updates should be handled
	// +kubebuilder:validation:Enum=UpdateAlways;UpdateIfNewer;Never
	// +kubebuilder:default=UpdateIfNewer
	SchemaUpdatePolicy SchemaUpdatePolicy `json:"schemaUpdatePolicy,omitempty"`
}

// ResourceSource specifies the source cluster and resource details
type ResourceSource struct {
	// Cluster is the name of the SyncTarget where the resource originates
	Cluster string `json:"cluster"`

	// Resource specifies the resource being imported
	Resource ResourceReference `json:"resource"`
}

// ResourceReference specifies a Kubernetes resource
type ResourceReference struct {
	// Group is the API group of the resource
	// +optional
	Group string `json:"group,omitempty"`

	// Version is the API version of the resource
	Version string `json:"version"`

	// Resource is the resource type (plural name)
	Resource string `json:"resource"`

	// Namespace is the namespace of the resource (if namespaced)
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the name of the resource
	Name string `json:"name"`
}

// SchemaUpdatePolicy defines how schema updates are handled
// +kubebuilder:validation:Enum=UpdateAlways;UpdateIfNewer;Never
type SchemaUpdatePolicy string

const (
	// SchemaUpdateAlways means always update the schema when changes are detected
	SchemaUpdateAlways SchemaUpdatePolicy = "UpdateAlways"

	// SchemaUpdateIfNewer means update the schema only if the version is newer
	SchemaUpdateIfNewer SchemaUpdatePolicy = "UpdateIfNewer"

	// SchemaUpdateNever means never update the schema automatically
	SchemaUpdateNever SchemaUpdatePolicy = "Never"
)

// ResourceImportStatus represents the observed state of the ResourceImport
type ResourceImportStatus struct {
	// Conditions represent the latest available observations of the import's current state
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// ResourceVersion is the resource version of the imported resource
	// +optional
	ResourceVersion string `json:"resourceVersion,omitempty"`

	// LastImportTime is when the resource was last imported
	// +optional
	LastImportTime *metav1.Time `json:"lastImportTime,omitempty"`

	// ImportedResource contains the actual imported resource data
	// +optional
	ImportedResource *runtime.RawExtension `json:"importedResource,omitempty"`

	// SchemaVersion is the version of the resource schema
	// +optional
	SchemaVersion string `json:"schemaVersion,omitempty"`
}

// ResourceExport represents a resource that should be exported from KCP
// to one or more SyncTargets for execution.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=re
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.target.clusters[0]`,description="Target cluster"
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=`.spec.resource.resource`,description="Resource type"
// +kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.resource.namespace`,description="Resource namespace"
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.resource.name`,description="Resource name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type ResourceExport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the ResourceExport
	Spec ResourceExportSpec `json:"spec,omitempty"`

	// Status represents the current state of the ResourceExport
	// +optional
	Status ResourceExportStatus `json:"status,omitempty"`
}

// ResourceExportSpec defines the desired state of the ResourceExport
type ResourceExportSpec struct {
	// Resource specifies the resource to be exported
	Resource ResourceReference `json:"resource"`

	// Target specifies where the resource should be exported
	Target ExportTarget `json:"target"`

	// ConflictResolution defines how conflicts should be handled
	// +kubebuilder:validation:Enum=UseSource;UseTarget;Error
	// +kubebuilder:default=UseSource
	ConflictResolution ConflictResolutionPolicy `json:"conflictResolution,omitempty"`
}

// ExportTarget specifies the target for resource export
type ExportTarget struct {
	// Clusters lists the target clusters for export
	// +optional
	Clusters []string `json:"clusters,omitempty"`

	// LocationSelector selects clusters based on location
	// +optional
	LocationSelector *LocationSelector `json:"locationSelector,omitempty"`

	// LabelSelector selects clusters based on labels
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`
}

// ConflictResolutionPolicy defines how conflicts are resolved
// +kubebuilder:validation:Enum=UseSource;UseTarget;Error
type ConflictResolutionPolicy string

const (
	// ConflictResolutionUseSource means use the source (KCP) version
	ConflictResolutionUseSource ConflictResolutionPolicy = "UseSource"

	// ConflictResolutionUseTarget means use the target (cluster) version
	ConflictResolutionUseTarget ConflictResolutionPolicy = "UseTarget"

	// ConflictResolutionError means error on conflicts
	ConflictResolutionError ConflictResolutionPolicy = "Error"
)

// ResourceExportStatus represents the observed state of the ResourceExport
type ResourceExportStatus struct {
	// Conditions represent the latest available observations of the export's current state
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// ExportedClusters tracks which clusters the resource has been exported to
	// +optional
	ExportedClusters []ClusterExportStatus `json:"exportedClusters,omitempty"`

	// LastExportTime is when the resource was last exported
	// +optional
	LastExportTime *metav1.Time `json:"lastExportTime,omitempty"`
}

// ClusterExportStatus tracks export status for a specific cluster
type ClusterExportStatus struct {
	// Cluster is the name of the target cluster
	Cluster string `json:"cluster"`

	// Exported indicates if the export was successful
	Exported bool `json:"exported"`

	// LastExportTime records the last export time for this cluster
	// +optional
	LastExportTime *metav1.Time `json:"lastExportTime,omitempty"`

	// Error records any export errors for this cluster
	// +optional
	Error string `json:"error,omitempty"`
}

// SyncTargetHeartbeat tracks the health and status of syncer connections
// to SyncTarget clusters.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,categories=kcp,shortName=sth
// +kubebuilder:printcolumn:name="SyncTarget",type=string,JSONPath=`.spec.syncTarget`,description="Associated SyncTarget"
// +kubebuilder:printcolumn:name="Last Heartbeat",type="date",JSONPath=`.status.lastHeartbeatTime`,description="Last heartbeat time"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready condition status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type SyncTargetHeartbeat struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec defines the desired state of the SyncTargetHeartbeat
	Spec SyncTargetHeartbeatSpec `json:"spec,omitempty"`

	// Status represents the current state of the SyncTargetHeartbeat
	// +optional
	Status SyncTargetHeartbeatStatus `json:"status,omitempty"`
}

// SyncTargetHeartbeatSpec defines the desired state of the SyncTargetHeartbeat
type SyncTargetHeartbeatSpec struct {
	// SyncTarget is the name of the associated SyncTarget
	SyncTarget string `json:"syncTarget"`

	// HeartbeatInterval specifies how often heartbeats should be sent
	// +kubebuilder:default="30s"
	HeartbeatInterval metav1.Duration `json:"heartbeatInterval,omitempty"`
}

// SyncTargetHeartbeatStatus represents the observed state of the SyncTargetHeartbeat
type SyncTargetHeartbeatStatus struct {
	// Conditions represent the latest available observations of the heartbeat's current state
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`

	// LastHeartbeatTime is the last time a heartbeat was received
	// +optional
	LastHeartbeatTime *metav1.Time `json:"lastHeartbeatTime,omitempty"`

	// SyncerIdentifier identifies the syncer sending heartbeats
	// +optional
	SyncerIdentifier string `json:"syncerIdentifier,omitempty"`

	// ClusterHealth provides information about cluster health
	// +optional
	ClusterHealth *ClusterHealthStatus `json:"clusterHealth,omitempty"`
}

// ClusterHealthStatus provides cluster health information
type ClusterHealthStatus struct {
	// Ready indicates if the cluster is ready to accept workloads
	Ready bool `json:"ready"`

	// NodeCount is the number of ready nodes in the cluster
	// +optional
	NodeCount *int32 `json:"nodeCount,omitempty"`

	// ResourceUsage provides current resource usage statistics
	// +optional
	ResourceUsage *ResourceUsageStatus `json:"resourceUsage,omitempty"`
}

// ResourceUsageStatus provides resource usage statistics
type ResourceUsageStatus struct {
	// CPUUsage in millicores
	// +optional
	CPUUsage *int64 `json:"cpuUsage,omitempty"`

	// MemoryUsage in bytes
	// +optional
	MemoryUsage *int64 `json:"memoryUsage,omitempty"`

	// StorageUsage in bytes
	// +optional
	StorageUsage *int64 `json:"storageUsage,omitempty"`

	// PodCount is the current number of pods
	// +optional
	PodCount *int32 `json:"podCount,omitempty"`
}

// Condition types and reasons for all TMC resources

// These are valid conditions for SyncTarget.
const (
	// SyncTargetReady means the SyncTarget is ready to accept workloads
	SyncTargetReady conditionsv1alpha1.ConditionType = "Ready"

	// SyncTargetSyncerReady means the syncer is connected and functional
	SyncTargetSyncerReady conditionsv1alpha1.ConditionType = "SyncerReady"

	// SyncTargetAPIImportsReady means all required API imports are available
	SyncTargetAPIImportsReady conditionsv1alpha1.ConditionType = "APIImportsReady"
)

// These are valid conditions for Placement.
const (
	// PlacementReady means the placement policy has been successfully applied
	PlacementReady conditionsv1alpha1.ConditionType = "Ready"

	// PlacementScheduled means clusters have been selected according to the policy
	PlacementScheduled conditionsv1alpha1.ConditionType = "Scheduled"
)

// These are valid conditions for Location.
const (
	// LocationReady means the location is ready and available for scheduling
	LocationReady conditionsv1alpha1.ConditionType = "Ready"

	// LocationClustersReady means all clusters at this location are ready
	LocationClustersReady conditionsv1alpha1.ConditionType = "ClustersReady"

	// LocationHierarchyValid means the location hierarchy is valid
	LocationHierarchyValid conditionsv1alpha1.ConditionType = "HierarchyValid"
)

// These are valid conditions for ResourceImport.
const (
	// ResourceImportReady means the resource has been successfully imported
	ResourceImportReady conditionsv1alpha1.ConditionType = "Ready"

	// ResourceImportSchemaReady means the resource schema is available
	ResourceImportSchemaReady conditionsv1alpha1.ConditionType = "SchemaReady"

	// ResourceImportSynced means the imported resource is in sync with the source
	ResourceImportSynced conditionsv1alpha1.ConditionType = "Synced"
)

// These are valid conditions for ResourceExport.
const (
	// ResourceExportReady means the resource has been successfully exported
	ResourceExportReady conditionsv1alpha1.ConditionType = "Ready"

	// ResourceExportSynced means the exported resource is in sync with targets
	ResourceExportSynced conditionsv1alpha1.ConditionType = "Synced"
)

// These are valid conditions for SyncTargetHeartbeat.
const (
	// SyncTargetHeartbeatReady means heartbeat monitoring is active
	SyncTargetHeartbeatReady conditionsv1alpha1.ConditionType = "Ready"
)

// Condition implementations for all TMC resources
func (st *SyncTarget) GetConditions() conditionsv1alpha1.Conditions {
	return st.Status.Conditions
}

func (st *SyncTarget) SetConditions(conditions conditionsv1alpha1.Conditions) {
	st.Status.Conditions = conditions
}

func (p *Placement) GetConditions() conditionsv1alpha1.Conditions {
	return p.Status.Conditions
}

func (p *Placement) SetConditions(conditions conditionsv1alpha1.Conditions) {
	p.Status.Conditions = conditions
}

func (l *Location) GetConditions() conditionsv1alpha1.Conditions {
	return l.Status.Conditions
}

func (l *Location) SetConditions(conditions conditionsv1alpha1.Conditions) {
	l.Status.Conditions = conditions
}

func (ri *ResourceImport) GetConditions() conditionsv1alpha1.Conditions {
	return ri.Status.Conditions
}

func (ri *ResourceImport) SetConditions(conditions conditionsv1alpha1.Conditions) {
	ri.Status.Conditions = conditions
}

func (re *ResourceExport) GetConditions() conditionsv1alpha1.Conditions {
	return re.Status.Conditions
}

func (re *ResourceExport) SetConditions(conditions conditionsv1alpha1.Conditions) {
	re.Status.Conditions = conditions
}

func (sth *SyncTargetHeartbeat) GetConditions() conditionsv1alpha1.Conditions {
	return sth.Status.Conditions
}

func (sth *SyncTargetHeartbeat) SetConditions(conditions conditionsv1alpha1.Conditions) {
	sth.Status.Conditions = conditions
}

// List types for all TMC resources

// SyncTargetList contains a list of SyncTarget
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SyncTargetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of SyncTarget objects.
	Items []SyncTarget `json:"items"`
}

// PlacementList contains a list of Placement
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of Placement objects.
	Items []Placement `json:"items"`
}

// LocationList contains a list of Location
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of Location objects.
	Items []Location `json:"items"`
}

// ResourceImportList contains a list of ResourceImport
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ResourceImportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ResourceImport objects.
	Items []ResourceImport `json:"items"`
}

// ResourceExportList contains a list of ResourceExport
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ResourceExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of ResourceExport objects.
	Items []ResourceExport `json:"items"`
}

// SyncTargetHeartbeatList contains a list of SyncTargetHeartbeat
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type SyncTargetHeartbeatList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	// Items is the list of SyncTargetHeartbeat objects.
	Items []SyncTargetHeartbeat `json:"items"`
}
