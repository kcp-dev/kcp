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
	"encoding/json"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// APIResourceSchema describes a resource, identified by (group, version, resource, schema).
//
// An APIResourceSchema is immutable and cannot be deleted if they are referenced by
// an APIExport in the same workspace.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type APIResourceSchema struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec holds the desired state.
	//
	// +optional
	Spec APIResourceSchemaSpec `json:"spec,omitempty"`
}

// APIResourceSchemaSpec defines the desired state of APIResourceSchema.
type APIResourceSchemaSpec struct {
	// group is the API group of the defined custom resource. Empty string means the
	// core API group. 	The resources are served under `/apis/<group>/...` or `/api` for the core group.
	//
	// +required
	Group string `json:"group"`

	// names specify the resource and kind names for the custom resource.
	//
	// +required
	Names apiextensionsv1.CustomResourceDefinitionNames `json:"names"`
	// scope indicates whether the defined custom resource is cluster- or namespace-scoped.
	// Allowed values are `Cluster` and `Namespaced`.
	//
	// +required
	// +kubebuilder:validation:Enum=Cluster;Namespaced
	Scope apiextensionsv1.ResourceScope `json:"scope"`

	// versions is the API version of the defined custom resource.
	//
	// Note: the OpenAPI v3 schemas must be equal for all versions until CEL
	//       version migration is supported.
	//
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	Versions []APIResourceVersion `json:"versions"`

	// nameValidation can be used to configure name validation for bound APIs.
	// Allowed values are `DNS1123Subdomain` and `PathSegmentName`.
	// - DNS1123Subdomain: a lowercase RFC 1123 subdomain must consist of lower case
	//   alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character.
	//   Regex used is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*'
	// - PathSegmentName: validates the name can be safely encoded as a path segment.
	//   The name may not be '.' or '..' and the name may not contain '/' or '%'.
	//
	// Defaults to `DNS1123Subdomain`, matching the behaviour of CRDs.
	//
	// +optional
	// +kubebuilder:validation:Enum=DNS1123Subdomain;PathSegmentName
	// +kubebuilder:default=DNS1123Subdomain
	NameValidation string `json:"nameValidation,omitempty"`
}

// APIResourceVersion describes one API version of a resource.
type APIResourceVersion struct {
	// name is the version name, e.g. “v1”, “v2beta1”, etc.
	// The custom resources are served under this version at `/apis/<group>/<version>/...` if `served` is true.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^v[1-9][0-9]*([a-z]+[1-9][0-9]*)?$
	Name string `json:"name"`
	// served is a flag enabling/disabling this version from being served via REST APIs
	//
	// +required
	// +kubebuilder:default=true
	Served bool `json:"served"`
	// storage indicates this version should be used when persisting custom resources to storage.
	// There must be exactly one version with storage=true.
	//
	// +required
	Storage bool `json:"storage"`

	//nolint:gocritic
	// deprecated indicates this version of the custom resource API is deprecated.
	// When set to true, API requests to this version receive a warning header in the server response.
	// Defaults to false.
	//
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`
	// deprecationWarning overrides the default warning returned to API clients.
	// May only be set when `deprecated` is true.
	// The default warning indicates this version is deprecated and recommends use
	// of the newest served version of equal or greater stability, if one exists.
	//
	// +optional
	DeprecationWarning *string `json:"deprecationWarning,omitempty"`
	// schema describes the structural schema used for validation, pruning, and defaulting
	// of this version of the custom resource.
	//
	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	// +structType=atomic
	Schema runtime.RawExtension `json:"schema"`
	// subresources specify what subresources this version of the defined custom resource have.
	//
	// +optional
	Subresources apiextensionsv1.CustomResourceSubresources `json:"subresources,omitempty"`
	// additionalPrinterColumns specifies additional columns returned in Table output.
	// See https://kubernetes.io/docs/reference/using-api/api-concepts/#receiving-resources-as-tables for details.
	// If no columns are specified, a single column displaying the age of the custom resource is used.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	AdditionalPrinterColumns []apiextensionsv1.CustomResourceColumnDefinition `json:"additionalPrinterColumns,omitempty"`
}

// APIResourceSchemaList is a list of APIResourceSchema resources
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type APIResourceSchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIResourceSchema `json:"items"`
}

func (v *APIResourceVersion) GetSchema() (*apiextensionsv1.JSONSchemaProps, error) {
	if v.Schema.Raw == nil {
		return nil, nil
	}
	var schema apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(v.Schema.Raw, &schema); err != nil {
		return nil, err
	}
	return &schema, nil
}

func (v *APIResourceVersion) SetSchema(schema *apiextensionsv1.JSONSchemaProps) error {
	if schema == nil {
		v.Schema.Raw = nil
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	v.Schema.Raw = raw
	return nil
}

const (
	// VersionPreservationAnnotationKeyPrefix is the prefix for the annotation key used to preserve fields from an API
	// version that would otherwise be lost during round-tripping to a different API version. An example key and value
	// might look like this: preserve.conversion.apis.kcp.io/v2: {"spec.someNewField": "someValue"}.
	VersionPreservationAnnotationKeyPrefix = "preserve.conversion.apis.kcp.io/"
)

// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// APIConversion contains rules to convert between different API versions in an APIResourceSchema. The name must match
// the name of the APIResourceSchema for the conversions to take effect.
type APIConversion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	// Spec holds the desired state.
	Spec APIConversionSpec `json:"spec"`
}

// APIConversionSpec contains rules to convert between different API versions in an APIResourceSchema.
type APIConversionSpec struct {
	// conversions specify rules to convert between different API versions in an APIResourceSchema.
	//
	// +required
	// +listType=map
	// +listMapKey=from
	// +listMapKey=to
	Conversions []APIVersionConversion `json:"conversions"`
}

// APIVersionConversion contains rules to convert between two specific API versions in an
// APIResourceSchema. Additionally, to avoid data loss when round-tripping from a version that
// contains a new field to one that doesn't and back again, you can specify a list of fields to
// preserve (these are stored in annotations).
type APIVersionConversion struct {
	// from is the source version.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^v[1-9][0-9]*([a-z]+[1-9][0-9]*)?$
	From string `json:"from"`

	// to is the target version.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=^v[1-9][0-9]*([a-z]+[1-9][0-9]*)?$
	To string `json:"to"`

	// rules contains field-specific conversion expressions.
	//
	// +required
	// +listType=map
	// +listMapKey=destination
	Rules []APIConversionRule `json:"rules"`

	// preserve contains a list of JSONPath expressions to fields to preserve in the originating version
	// of the object, relative to its root, such as '.spec.name.first'.
	//
	// +optional
	Preserve []string `json:"preserve,omitempty"`
}

// APIConversionRule specifies how to convert a single field.
type APIConversionRule struct {
	// field is a JSONPath expression to the field in the originating version of the object, relative to its root, such
	// as '.spec.name.first'.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Field string `json:"field"`

	// destination is a JSONPath expression to the field in the target version of the object, relative to
	// its root, such as '.spec.name.first'.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	Destination string `json:"destination"`

	// transformation is an optional CEL expression used to execute user-specified rules to transform the
	// originating field -- identified by 'self' -- to the destination field.
	//
	// +optional
	Transformation string `json:"transformation,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// APIConversionList is a list of APIConversion resources.
type APIConversionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []APIConversion `json:"items"`
}
