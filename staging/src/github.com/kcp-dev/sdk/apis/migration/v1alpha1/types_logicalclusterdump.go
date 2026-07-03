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
)

// LogicalClusterDump is an ephemeral request/response type used by the
// destination shard during a logical cluster migration to fetch the raw
// etcd contents of the migration logical cluster from the origin shard.
//
// The server populates Status.Entries on Create. The object is not
// persisted.
//
// +genclient
// +genclient:nonNamespaced
// +genclient:onlyVerbs=create
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LogicalClusterDump struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec LogicalClusterDumpSpec `json:"spec,omitempty"`
	// +optional
	Status LogicalClusterDumpStatus `json:"status,omitempty"`
}

// LogicalClusterDumpSpec is the desired state for a dump request.
//
// The logical cluster to dump is taken from the request's cluster context
// (i.e. the URL the request arrived on).
type LogicalClusterDumpSpec struct {
	// continue is the token returned by a previous LogicalClusterDump's
	// status.continue. If set, the scan resumes right after the last key
	// returned by that page. If empty, the scan starts from the
	// beginning of the logical cluster's etcd keyspace.
	//
	// +optional
	Continue string `json:"continue,omitempty"`

	// limit caps the number of entries returned in a single page. If
	// zero, the server picks a default page size.
	//
	// +optional
	Limit int64 `json:"limit,omitempty"`

	// maxBytes caps the total size, in bytes, of entry values returned in
	// a single page. The scan stops and returns a continue token as soon
	// as this budget is exceeded, even if limit hasn't been reached yet.
	// If zero, the server picks a default byte budget.
	//
	// +optional
	MaxBytes int64 `json:"maxBytes,omitempty"`
}

// LogicalClusterDumpStatus carries the dump payload populated by the server.
type LogicalClusterDumpStatus struct {
	// entries is the page of etcd key/value pairs belonging to the
	// logical cluster, in the origin shard's etcd encoding (proto for
	// built-ins, JSON for CRs).
	//
	// +optional
	Entries []EtcdEntry `json:"entries,omitempty"`

	// continue is set when there are more entries to fetch. Pass it as
	// spec.continue on the next LogicalClusterDump request to get the
	// next page. Empty means this was the last page.
	//
	// +optional
	Continue string `json:"continue,omitempty"`
}

// EtcdEntry is a single etcd key/value pair from the origin shard.
type EtcdEntry struct {
	// key is the etcd key with the origin shard's storage prefix stripped.
	// The destination shard prepends its own storage prefix before writing.
	Key string `json:"key"`

	// value is the raw etcd value bytes. JSON-encoded as base64 on the wire.
	Value []byte `json:"value"`
}
