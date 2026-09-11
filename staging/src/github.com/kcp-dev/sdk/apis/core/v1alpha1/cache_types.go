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

	"github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// Cache describes a kcp cache instance
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type Cache struct {
	v1.TypeMeta `json:",inline"`
	// +optional
	v1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec CacheSpec `json:"spec,omitempty"`

	// +optional
	Status CacheStatus `json:"status,omitempty"`
}

func (in *Cache) SetConditions(c v1alpha1.Conditions) {
	in.Status.Conditions = c
}

func (in *Cache) GetConditions() v1alpha1.Conditions {
	return in.Status.Conditions
}

var _ conditions.Getter = &Cache{}
var _ conditions.Setter = &Cache{}

// CacheSpec holds the desired state of the Cache.
type CacheSpec struct {
	// BaseURL is the internal address the front-proxy uses to reach this cache instance.
	BaseURL string `json:"baseURL"`
	// ExternalURL is the externally-visible address (optional).
	ExternalURL string `json:"externalURL,omitempty"`
}

// CacheStatus communicates the observed state of the Cache.
type CacheStatus struct {
	// Current processing state of the Shard.
	// +optional
	Conditions v1alpha1.Conditions `json:"conditions,omitempty"`
}

// CacheList is a list of cache instances
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CacheList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata"`

	Items []Cache `json:"items"`
}
