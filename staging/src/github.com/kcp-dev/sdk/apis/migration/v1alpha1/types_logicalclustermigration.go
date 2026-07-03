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
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// LogicalClusterMigration describes a migration of a logical cluster from one shard to another.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Logical Cluster",type=string,JSONPath=`.spec.logicalCluster`,description="The logical cluster being migrated"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`,description="The current phase of the migration"
// +kubebuilder:printcolumn:name="Destination",type=string,JSONPath=`.spec.destinationShard`,description="The destination shard"
// +kubebuilder:printcolumn:name="Entries Copied",type=integer,JSONPath=`.status.entriesCopied`,description="Number of etcd entries copied so far"
type LogicalClusterMigration struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Spec LogicalClusterMigrationSpec `json:"spec,omitempty"`
	// +optional
	Status LogicalClusterMigrationStatus `json:"status,omitempty"`
}

// LogicalClusterMigrationSpec holds the desired state of the migration.
type LogicalClusterMigrationSpec struct {
	// logicalCluster is the name of the logical cluster to migrate.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	LogicalCluster string `json:"logicalCluster"`

	// destinationShard is the name of the shard to migrate the logical cluster to.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	DestinationShard string `json:"destinationShard"`
}

// LogicalClusterMigrationPhaseType is the type of the current phase of the migration.
//
// +kubebuilder:validation:Enum=Preparing;Migrating;OriginCleanup;DestinationFinalize;Completed;Failed
type LogicalClusterMigrationPhaseType string

const (
	LogicalClusterMigrationPhasePreparing           LogicalClusterMigrationPhaseType = "Preparing"
	LogicalClusterMigrationPhaseMigrating           LogicalClusterMigrationPhaseType = "Migrating"
	LogicalClusterMigrationPhaseOriginCleanup       LogicalClusterMigrationPhaseType = "OriginCleanup"
	LogicalClusterMigrationPhaseDestinationFinalize LogicalClusterMigrationPhaseType = "DestinationFinalize"
	LogicalClusterMigrationPhaseCompleted           LogicalClusterMigrationPhaseType = "Completed"
	LogicalClusterMigrationPhaseFailed              LogicalClusterMigrationPhaseType = "Failed"
)

// LogicalClusterMigrationStatus communicates the observed state of the migration.
type LogicalClusterMigrationStatus struct {
	// phase is the current phase of the migration.
	//
	// +optional
	// +kubebuilder:default=Preparing
	Phase LogicalClusterMigrationPhaseType `json:"phase,omitempty"`

	// originShard is the name of the shard to migrate the logical cluster from.
	// Set by the origin shard controller during preparation.
	//
	// +optional
	OriginShard string `json:"originShard,omitempty"`

	// entriesCopied is the number of etcd entries copied from the origin
	// shard to the destination shard so far during the Migrating phase.
	//
	// +optional
	EntriesCopied int64 `json:"entriesCopied,omitempty"`

	// dumpContinue is the continue token for the next LogicalClusterDump
	// page to fetch from the origin shard. Set by the destination shard
	// controller as it works through the data copy, and cleared once the
	// copy is complete. Used to resume the copy from where it left off
	// after a destination shard restart, instead of starting over.
	//
	// +optional
	DumpContinue string `json:"dumpContinue,omitempty"`

	// Current processing state of the migration.
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

const (
	// LCMigrationOriginPreparing indicates the origin shard is preparing for migration.
	LCMigrationOriginPreparing conditionsv1alpha1.ConditionType = "OriginPreparing"

	// LCMigrationOriginReady indicates the origin shard has completed preparation
	// and the destination shard can start copying data.
	LCMigrationOriginReady conditionsv1alpha1.ConditionType = "OriginReady"

	// LCMigrationStarted indicates the destination shard has started copying data
	// from the origin.
	LCMigrationStarted conditionsv1alpha1.ConditionType = "MigrationStarted"

	// LCMigrationDataCopied indicates the destination shard has finished copying
	// and verifying all data.
	LCMigrationDataCopied conditionsv1alpha1.ConditionType = "DataCopied"

	// LCMigrationOriginCleaned indicates the origin shard has deleted all objects
	// belonging to the migration logical cluster.
	LCMigrationOriginCleaned conditionsv1alpha1.ConditionType = "OriginCleaned"

	// LCMigrationCompleted indicates the migration has fully completed and the
	// logical cluster is available on the destination shard.
	LCMigrationCompleted conditionsv1alpha1.ConditionType = "Completed"
)

func (in *LogicalClusterMigration) SetConditions(c conditionsv1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *LogicalClusterMigration) GetConditions() conditionsv1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &LogicalClusterMigration{}
var _ conditions.Setter = &LogicalClusterMigration{}

// LogicalClusterMigrationList is a list of LogicalClusterMigration resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LogicalClusterMigrationList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []LogicalClusterMigration `json:"items"`
}
