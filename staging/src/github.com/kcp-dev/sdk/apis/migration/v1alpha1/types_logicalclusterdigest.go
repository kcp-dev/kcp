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

// LogicalClusterDigest is an ephemeral request/response type used by the
// destination shard during a logical cluster migration to fetch key/hash
// digests of the migrating logical cluster from the origin shard after the
// data copy completes.
//
// The server populates Status.Entries on Create. The object is not
// persisted.
//
// +genclient
// +genclient:nonNamespaced
// +genclient:onlyVerbs=create
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type LogicalClusterDigest struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec LogicalClusterDigestSpec `json:"spec,omitempty"`
	// +optional
	Status LogicalClusterDigestStatus `json:"status,omitempty"`
}

// LogicalClusterDigestSpec is the desired state for a digest request.
//
// The logical cluster to inspect is taken from the request's cluster context
// (i.e. the URL the request arrived on).
type LogicalClusterDigestSpec struct {
	// continue is the token returned by a previous LogicalClusterDigest's
	// status.continue. If set, the scan resumes at that key. If empty,
	// the scan starts from the beginning of the logical cluster's etcd
	// keyspace.
	//
	// +optional
	Continue string `json:"continue,omitempty"`

	// limit caps the number of digest entries returned in a single page.
	// If zero, the server picks a default page size.
	//
	// +optional
	Limit int64 `json:"limit,omitempty"`
}

// LogicalClusterDigestStatus carries the digest payload populated by the server.
type LogicalClusterDigestStatus struct {
	// entries is the page of etcd key digests belonging to the logical
	// cluster, computed over the plaintext (unencrypted) values.
	//
	// +optional
	Entries []EtcdEntryDigest `json:"entries,omitempty"`

	// continue is set when there are more entries to fetch. Pass it as
	// spec.continue on the next LogicalClusterDigest request to get the
	// next page. Empty means this was the last page.
	//
	// +optional
	Continue string `json:"continue,omitempty"`
}

// EtcdEntryDigest is a single etcd key and the SHA-256 digest of its plaintext
// value.
type EtcdEntryDigest struct {
	// key is the etcd key with the shard's storage prefix stripped.
	Key string `json:"key"`

	// hash is the hex-encoded SHA-256 digest of the entry's plaintext value
	// (with storage encryption removed).
	Hash string `json:"hash"`
}
