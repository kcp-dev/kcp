// Copyright 2018
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package crdpuller

import (
	"context"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/util/proto"
	"k8s.io/kube-openapi/pkg/util/sets"
)

type SchemaPuller interface {
	PullCRDs(context context.Context, resourceNames ...string) (map[string]*apiextensionsv1.CustomResourceDefinition, error)
}

type schemaPuller struct {
	discoveryClient *discovery.DiscoveryClient
	crdClient       *apiextensionsv1client.ApiextensionsV1Client
	models          proto.Models
}

var _ SchemaPuller = &schemaPuller{}

func NewSchemaPuller(config *rest.Config) (SchemaPuller, error) {
	crdClient, err := apiextensionsv1client.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	openapiSchema, err := discoveryClient.OpenAPISchema()
	if err != nil {
		return nil, err
	}
	models, err := proto.NewOpenAPIData(openapiSchema)
	if err != nil {
		return nil, err
	}

	return &schemaPuller{
		discoveryClient: discoveryClient,
		crdClient: crdClient,
		models:          models,
	}, nil
}

func (sp *schemaPuller) PullCRDs(context context.Context, resourceNames ...string) (map[string]*apiextensionsv1.CustomResourceDefinition, error) {
	crds := map[string]*apiextensionsv1.CustomResourceDefinition{}
	_, apiResourcesLists, err := sp.discoveryClient.ServerGroupsAndResources()
	if err != nil {
		return nil, err
	}

	apiResourceNames := map[schema.GroupVersion]sets.String {}
	for _, apiResourcesList := range apiResourcesLists {
		gv, err := schema.ParseGroupVersion(apiResourcesList.GroupVersion)
		if err != nil {
			continue
		}

		apiResourceNames[gv] = sets.NewString()
		for _, apiResource := range apiResourcesList.APIResources {
			apiResourceNames[gv].Insert(apiResource.Name)
		}
		 
	}

	apiResourcesLists, err = sp.discoveryClient.ServerPreferredResources()
	if err != nil {
		return nil, err
	}
	for _, apiResourcesList := range apiResourcesLists {
		gv, err := schema.ParseGroupVersion(apiResourcesList.GroupVersion)
		if err != nil {
			continue
		}

		for _, apiResource := range apiResourcesList.APIResources {
			for _, resourceName := range resourceNames {
				if resourceName != apiResource.Name {
					continue
				}
				CRDName := apiResource.Name
				if gv.Group == "" {
					CRDName = CRDName + ".core"
				} else {
					CRDName = CRDName + "." + gv.Group
				}

				objectMeta := metav1.ObjectMeta{
					Name:        CRDName,
					Labels:      map[string]string{},
					Annotations: map[string]string{},
				}

				typeMeta := metav1.TypeMeta{
					Kind:       "CustomResourceDefinition",
					APIVersion: "apiextensions.k8s.io/v1",
				}

				crd, err := sp.crdClient.CustomResourceDefinitions().Get(context, CRDName, metav1.GetOptions{})
				if err == nil {
					crds[apiResource.Name] = &apiextensionsv1.CustomResourceDefinition{
						TypeMeta: typeMeta,
						ObjectMeta: objectMeta,
						Spec: crd.Spec,
					}
					continue
				} else {
					if !errors.IsNotFound(err) {
						return nil, err
					}
				}

				var resourceScope apiextensionsv1.ResourceScope
				if apiResource.Namespaced {
					resourceScope = apiextensionsv1.NamespaceScoped
				} else {
					resourceScope = apiextensionsv1.ClusterScoped
				}
				swaggerSpecDefinitionName := gv.Group
				if swaggerSpecDefinitionName == "" {
					swaggerSpecDefinitionName = "core"
				}
				if !strings.Contains(swaggerSpecDefinitionName, ".") {
					swaggerSpecDefinitionName = "io.k8s.api." + swaggerSpecDefinitionName
				}
				swaggerSpecDefinitionName = swaggerSpecDefinitionName + "." + gv.Version + "." + apiResource.Kind

				protoSchema := sp.models.LookupModel(swaggerSpecDefinitionName)
				schemaProps := apiextensionsv1.JSONSchemaProps{}
				protoSchema.Accept(&SchemaConverter{
					schemaProps: &schemaProps,
					schemaName:  swaggerSpecDefinitionName,
				})

				hasSubResource := func(subResource string) bool {
					groupResourceNames := apiResourceNames[gv]
					if groupResourceNames != nil {
						return groupResourceNames.Has(apiResource.Name + "/" + subResource)
					}
					return false
				}
		
				statusSubResource := &apiextensionsv1.CustomResourceSubresourceStatus{}
				if ! hasSubResource("status") {
					statusSubResource = nil
				}

				scaleSubResource := &apiextensionsv1.CustomResourceSubresourceScale{
					SpecReplicasPath:   ".replicas",
					StatusReplicasPath: ".replicas",
				}
				if ! hasSubResource("scale") {
					scaleSubResource = nil
				}

				crds[apiResource.Name] = &apiextensionsv1.CustomResourceDefinition{
					TypeMeta: typeMeta,
					ObjectMeta: objectMeta,
					Spec: apiextensionsv1.CustomResourceDefinitionSpec{
						Group: apiResource.Group,
						Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
							{
								Name: gv.Version,
								Schema: &apiextensionsv1.CustomResourceValidation{
									OpenAPIV3Schema: &schemaProps,
								},
								Subresources: &apiextensionsv1.CustomResourceSubresources{
									Status: statusSubResource,
									Scale:  scaleSubResource,
								},
								Served:  true,
								Storage: true,
							},
						},
						Scope: resourceScope,
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Plural:     apiResource.Name,
							Kind:       apiResource.Kind,
							Categories: apiResource.Categories,
							ShortNames: apiResource.ShortNames,
							Singular:   apiResource.SingularName,
							ListKind:   apiResource.Kind + "List",
						},
					},
				}

				break
			}
		}
	}
	return crds, nil
}

type SchemaConverter struct {
	schemaProps *apiextensionsv1.JSONSchemaProps
	schemaName  string
	description string
}

var _ proto.SchemaVisitorArbitrary = (*SchemaConverter)(nil)

func (sc *SchemaConverter) setupDescription(schema proto.Schema) {
	schemaDescription := schema.GetDescription() 
	inheritedDescription := sc.description
	subSchemaDescription := "" 

	switch typed := schema.(type) {
	case *proto.Arbitrary:
	case *proto.Array:
		subSchemaDescription = typed.SubType.GetDescription()
	case *proto.Primitive:
	case *proto.Kind:
	case *proto.Map:
		subSchemaDescription = typed.SubType.GetDescription()
	case *proto.Ref:
		subSchemaDescription = typed.SubSchema().GetDescription()
	}

	if inheritedDescription != "" {
		sc.schemaProps.Description = inheritedDescription
	} else if subSchemaDescription != "" {
		sc.schemaProps.Description = subSchemaDescription
	} else {
		sc.schemaProps.Description = schemaDescription
	}
}

func (sc *SchemaConverter) VisitArbitrary(a *proto.Arbitrary) {
	sc.setupDescription(a)
	sc.schemaProps.XEmbeddedResource = true
	if a.Extensions != nil {
		if preserveUnknownFields, exists := a.Extensions["x-kubernetes-preserve-unknown-fields"]; exists {
			boolValue := preserveUnknownFields.(bool)
			sc.schemaProps.XPreserveUnknownFields = &boolValue
		}
	}
}

func (sc *SchemaConverter) VisitArray(a *proto.Array) {
	sc.setupDescription(a)	
	sc.schemaProps.Type = "array"
	if len(a.Extensions) > 0 {
		if val := a.Extensions["x-kubernetes-list-type"]; val != nil {
			listType := val.(string)
			sc.schemaProps.XListType = &listType
		} else if val := a.Extensions["x-kubernetes-patch-strategy"]; val != nil && val != "" {
			listType := "atomic"
			if val.(string) == "merge" || strings.HasPrefix(val.(string), "merge,") {
				if _, isPrimitive := a.SubType.(*proto.Primitive); isPrimitive {
					listType = "set"
				} else {
					listType = "map"
				}
			}
			sc.schemaProps.XListType = &listType
		}
		if val := a.Extensions["x-kubernetes-list-map-keys"]; val != nil {
			listMapKeys := val.([]interface{})
			for _, key := range listMapKeys {
				sc.schemaProps.XListMapKeys = append(sc.schemaProps.XListMapKeys, key.(string))
			}
		} else if val := a.Extensions["x-kubernetes-patch-merge-key"]; val != nil {
			sc.schemaProps.XListMapKeys = []string{val.(string)}
			if a.Extensions["x-kubernetes-patch-strategy"] == nil {
				listType := "map"
				sc.schemaProps.XListType = &listType
			}
		}
		if len(sc.schemaProps.XListMapKeys) > 0 {
			var kind *proto.Kind = nil
			switch subType := a.SubType.(type) {
			case *proto.Ref:
				refSchema := subType.SubSchema()
				if aKind, isKind := refSchema.(*proto.Kind); isKind {
					kind = aKind
				}
			case *proto.Kind:
				kind = subType
			}
			if kind != nil {
				required := sets.NewString(kind.RequiredFields...)
				required.Insert(sc.schemaProps.XListMapKeys...)
				kind.RequiredFields = required.List()
			}
		}
	}
	subtypeSchemaProps := apiextensionsv1.JSONSchemaProps{}
	a.SubType.Accept(&SchemaConverter{
		schemaProps: &subtypeSchemaProps,
		schemaName:  sc.schemaName,
		description: a.SubType.GetDescription(),
	})
	sc.schemaProps.Items = &apiextensionsv1.JSONSchemaPropsOrArray{
		Schema: &subtypeSchemaProps,
	}
}
func (sc *SchemaConverter) VisitMap(m *proto.Map) {
	sc.setupDescription(m)	
	subtypeSchemaProps := apiextensionsv1.JSONSchemaProps{}
	m.SubType.Accept(&SchemaConverter{
		schemaProps: &subtypeSchemaProps,
		schemaName:  sc.schemaName,
		description: m.SubType.GetDescription(),
	})
	sc.schemaProps.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{
		Schema: &subtypeSchemaProps,
	}
	sc.schemaProps.Type = "object"
}
func (sc *SchemaConverter) VisitPrimitive(p *proto.Primitive) {
	sc.setupDescription(p)
	sc.schemaProps.Type = p.Type
	sc.schemaProps.Format = p.Format
}

func (sc *SchemaConverter) VisitKind(k *proto.Kind) {
	sc.setupDescription(k)
	sc.schemaProps.Required = k.RequiredFields
	sc.schemaProps.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	sc.schemaProps.Type = "object"
	for fieldName, field := range k.Fields {
		fieldSchemaProps := apiextensionsv1.JSONSchemaProps{}

		path := field.GetPath().String()
		if path == sc.schemaName+".metadata" {
			fieldSchemaProps.Type = "object"
		} else {
			field.Accept(&SchemaConverter{
				schemaProps: &fieldSchemaProps,
				schemaName:  sc.schemaName,
				description: field.GetDescription(),
			})
		}

		sc.schemaProps.Properties[fieldName] = fieldSchemaProps
	}
	for extensionName, extension := range k.Extensions {
		switch extensionName {
		case "x-kubernetes-patch-merge-key":
			sc.schemaProps.XListMapKeys = []string{extension.(string)}
		case "x-kubernetes-list-map-keys":
			sc.schemaProps.XListMapKeys = extension.([]string)
		case "x-kubernetes-list-type":
			val := extension.(string)
			sc.schemaProps.XListType = &val
		}
	}
}

func (sc *SchemaConverter) VisitReference(r proto.Reference) {
	reference := r.Reference()
	if knownSchema, schemaIsKnown := knownSchemas[reference]; schemaIsKnown {
		knownSchema.DeepCopyInto(sc.schemaProps)
		if sc.description != "" {
			sc.schemaProps.Description = sc.description
		} else {
			sc.schemaProps.Description = r.GetDescription()
		}
		return
	}
	sc.setupDescription(r)
	r.SubSchema().Accept(sc)
}

func boolPtr(b bool) *bool {
	return &b
}

var knownPackages map[string]map[string]apiextensionsv1.JSONSchemaProps = map[string]map[string]apiextensionsv1.JSONSchemaProps {
	"k8s.io/api/core/v1": {
		// Explicit defaulting for the corev1.Protocol type in lieu of https://github.com/kubernetes/enhancements/pull/1928
		"Protocol": apiextensionsv1.JSONSchemaProps{
			Type:    "string",
			Default: &apiextensionsv1.JSON{Raw: []byte(`"TCP"`)},
		},
	},

	"k8s.io/apimachinery/pkg/apis/meta/v1": {
		// ObjectMeta is managed by the Kubernetes API server, so no need to
		// generate validation for it.
		"ObjectMeta": apiextensionsv1.JSONSchemaProps{
			Type: "object",
		},
		"Time": apiextensionsv1.JSONSchemaProps{
			Type:   "string",
			Format: "date-time",
		},
		"MicroTime": apiextensionsv1.JSONSchemaProps{
			Type:   "string",
			Format: "date-time",
		},
		"Duration": apiextensionsv1.JSONSchemaProps{
			// TODO(directxman12): regexp validation for this (or get kube to support it as a format value)
			Type: "string",
		},
		"Fields": apiextensionsv1.JSONSchemaProps{
			// this is a recursive structure that can't be flattened or, for that matter, properly generated.
			// so just treat it as an arbitrary map
			Type:                 "object",
			AdditionalProperties: &apiextensionsv1.JSONSchemaPropsOrBool{Allows: true},
		},
	},

	"k8s.io/apimachinery/pkg/api/resource": {
		"Quantity": apiextensionsv1.JSONSchemaProps{
			// TODO(directxman12): regexp validation for this (or get kube to support it as a format value)
			XIntOrString: true,
			AnyOf: []apiextensionsv1.JSONSchemaProps{
				{Type: "integer"},
				{Type: "string"},
			},
			Pattern: "^(\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))))?$",
		},
		// No point in calling AddPackage, this is the sole inhabitant
	},

	"k8s.io/apimachinery/pkg/runtime": {
		"RawExtension": apiextensionsv1.JSONSchemaProps{
			// TODO(directxman12): regexp validation for this (or get kube to support it as a format value)
			Type: "object",
		},
	},

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured": {
		"Unstructured": apiextensionsv1.JSONSchemaProps{
			Type: "object",
		},
	},

	"k8s.io/apimachinery/pkg/util/intstr": {
		"IntOrString": apiextensionsv1.JSONSchemaProps{
			XIntOrString: true,
			AnyOf: []apiextensionsv1.JSONSchemaProps{
				{Type: "integer"},
				{Type: "string"},
			},
		},
		// No point in calling AddPackage, this is the sole inhabitant
	},

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1": {
		"JSON": apiextensionsv1.JSONSchemaProps{
			XPreserveUnknownFields: boolPtr(true),
		},
	},
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1": {
		"JSON": apiextensionsv1.JSONSchemaProps{
			XPreserveUnknownFields: boolPtr(true),
		},
	},
}

var knownSchemas map[string]apiextensionsv1.JSONSchemaProps

func init() {
	knownSchemas = map[string]apiextensionsv1.JSONSchemaProps {}
	for pkgName, schemas := range knownPackages {
		for typeName, schema := range schemas {
			schemaName := util.ToRESTFriendlyName(pkgName + "." + typeName)
			knownSchemas[schemaName] = schema
		}
	}
}