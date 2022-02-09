/*
Copyright 2022 The KCP Authors.

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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

// ServiceBinding projects APIs and backing implementations into a Workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ServiceBinding struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec ServiceBindingSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status ServiceBindingStatus `json:"status,omitempty"`
}

// ServiceBindingSpec records the APIs and implementations that are to be bound.
type ServiceBindingSpec struct {
	// Services describe the APIs and implementations that are to be bound.
	Services []ServiceSpec `json:"services,omitempty"`
}

// ServiceSpec describes one API and backing implementation.
type ServiceSpec struct {
	// TODO: use a YAML patcher as a post-processing step to add oneOf validation to the CRD YAML, ex:
	// https://github.com/openshift/build-machinery-go/blob/7e33a7eb4ce3955ce3500d6daa96859384d0702b/make/targets/openshift/yaml-patch.mk
	// https://github.com/openshift/build-machinery-go/blob/6ed14425650f63995829a6c95c7ad3a23e589046/make/targets/openshift/operator/profile-manifests.mk#L17-L25
	// https://github.com/openshift/build-machinery-go/blob/6ed14425650f63995829a6c95c7ad3a23e589046/make/targets/openshift/operator/profile-manifests.mk#L34
	// https://github.com/openshift/api/blob/14f26e4285a42be94433c57dfea792cb6acda410/networkoperator/v1/001-egressrouter.crd.yaml-patch
	Published *PublishedServiceSpec `json:"published,omitempty"`
	Local     *LocalServiceSpec     `json:"local,omitempty"`
	External  *ExternalServiceSpec  `json:"external,omitempty"`
}

// PublishedServiceSpec describes an API and backing implementation that are provided by an actor on kcp.
type PublishedServiceSpec struct {
	// Name of the ServiceExport that describes the API for this PublishedServiceSpec.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Workspace of the ServiceExport that describes the API for this PublishedServiceSpec.
	//
	// +kubebuilder:validation:Required
	Workspace string `json:"workspace"`
}

// LocalServiceSpec describes an API and backing implementation that are provided by an actor in the
// same Workspace as the ServiceBinding.
type LocalServiceSpec struct {
	// Name of the ServiceExport that describes the API for this LocalServiceSpec.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// ExternalServiceSpec describes an API and backing implementation that are provided by a third-party actor.
type ExternalServiceSpec struct {
	// URL at which this external service may be reached.
	//
	// +kubebuilder:validation:Format:uri
	// +kubebuilder:validation:Required
	URL string `json:"url"`
}

// ServiceBindingStatus records which schemas are bound.
type ServiceBindingStatus struct {
	// BoundServices records the state of binding individual services.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +patchStrategy=merge
	// +patchMergeKey=name
	BoundServices []BoundService `json:"boundServices,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// BoundService describes a bound ServiceExport.
type BoundService struct {
	// Name is the name of the ServiceExport which is bound.
	//
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// BoundResourceSchemas record which resource schemas are bound for this ServiceExport.
	//
	// +optional
	// +listType=map
	// +listMapKey=schema
	// +patchStrategy=merge
	// +patchMergeKey=schema
	BoundResourceSchemas []ResourceSchemaReference `json:"boundResourceSchemas,omitempty" patchStrategy:"merge" patchMergeKey:"schema"`
	// Current binding state of the ServiceExport.
	//
	// +optional
	Conditions conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// ResourceSchemaReference records a resource, identified by (group, version, resource, schema).
type ResourceSchemaReference struct {
	metav1.GroupVersionResource `json:",inline"`

	// Schema is the name of the bound ResourceSchema.
	Schema string `json:"schema,omitempty"`
	// Hash is
	Hash string `json:"hash,omitempty"`
}

// ServiceBindingList is a list of ServiceBinding resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ServiceBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ServiceBinding `json:"items"`
}

// ServiceExport registers an API and implementation to allow consumption by others.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ServiceExport struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec ServiceExportSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status ServiceExportStatus `json:"status,omitempty"`
}

type ServiceExportSpec struct {
	// ResourceSchemas records which resource schemas are exposed with this ServiceExport.
	//
	// +optional
	// +listType=map
	// +listMapKey=schema
	// +patchStrategy=merge
	// +patchMergeKey=schema
	ResourceSchemas []ResourceSchemaReference `json:"resourceSchemas,omitempty" patchStrategy:"merge" patchMergeKey:"schema"`
}

type ServiceExportStatus struct {
	// ResourceSchemasInUse records which schemas are actually in use (that is, object exist at that schema in some
	// Workspace that binds this ServiceExport).
	//
	// +optional
	ResourceSchemasInUse []string `json:"resourceSchemasInUse,omitempty"`
}

// ServiceExportList is a list of ServiceExport resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ServiceExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ServiceExport `json:"items"`
}

// ResourceSchema describes a resource, identified by (group, version, resource, schema).
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kcp
type ResourceSchema struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	// +optional
	Spec ResourceSchemaSpec `json:"spec,omitempty"`

	// Status communicates the observed state.
	// +optional
	Status ResourceSchemaStatus `json:"status,omitempty"`
}

type ResourceSchemaSpec struct {
	// group is the API group of the defined custom resource.
	// The custom resources are served under `/apis/<group>/...`.
	// Must match the name of the CustomResourceDefinition (in the form `<names.plural>.<group>`).
	Group string `json:"group"`
	// names specify the resource and kind names for the custom resource.
	Names apiextensionsv1.CustomResourceDefinitionNames `json:"names"`
	// scope indicates whether the defined custom resource is cluster- or namespace-scoped.
	// Allowed values are `Cluster` and `Namespaced`.
	Scope apiextensionsv1.ResourceScope `json:"scope"`
	// version is the API version of the defined custom resource.
	Version apiextensionsv1.CustomResourceDefinitionVersion `json:"version"`
}

type ResourceSchemaStatus struct {
}

// ResourceSchemaList is a list of ResourceSchema resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ResourceSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []ServiceExport `json:"items"`
}
