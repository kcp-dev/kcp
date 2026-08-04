/*
Copyright 2025 The kcp Authors.

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
)

// Endpoint is one URL an endpoint slice advertises, and the shards it is meant
// for.
//
// Endpoint slices are read unstructured where the kind is only known at runtime
// -- an APIExport may point at any kind that follows this shape -- so this is
// the contract those readers rely on: status.endpoints[] with a url, and
// optionally a shards selector saying which shards that url serves.
type Endpoint struct {
	// url is the virtual workspace URL serving this endpoint.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:format:URL
	// +required
	URL string `json:"url"`

	// shards says which shards this URL serves.
	//
	// Saying nothing -- the zero value -- means the URL was composed for one
	// specific shard and is matched by URL prefix instead, which is what kcp's
	// own controllers publish. A status should not mix the two: either every
	// endpoint says which shards it is for, or none does.
	//
	// +optional
	Shards EndpointSelector `json:"shards,omitempty"`
}

// EndpointSelector says which shards an endpoint URL serves.
//
// It offers two ways of saying it, and saying both is an error: matchAll is the
// whole installation, and selector is a subset of it. Saying neither says
// nothing about shards at all, and leaves the URL to be matched by prefix.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.matchAll) && self.matchAll && has(self.selector))",message="matchAll and selector are mutually exclusive"
type EndpointSelector struct {
	// matchAll says this URL serves every shard: one virtual workspace for the
	// whole installation, which is what a provider running a single virtual
	// workspace publishes.
	//
	// +optional
	MatchAll bool `json:"matchAll,omitempty"`

	// selector picks the shards this URL serves by matching against shard
	// labels. Every shard labels itself with its own name, so a single shard is
	// selected with matchLabels: {name: <shard>}, and groupings such as a region
	// use whatever labels the installation puts on its shards -- the same ones
	// Partition and PartitionSet select by.
	//
	// The most specific selector that matches a shard wins, so a URL for a
	// region overrides one for the whole installation where the region applies.
	//
	// +optional
	Selector *v1.LabelSelector `json:"selector,omitempty"`
}
