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

package crdpuller

// We import the generic control plane scheme to provide access to the KCP control plane scheme,
// that gathers a minimal set of Kubernetes APIs without any workload-related APIs.
//
// We don't want to import, from physical clusters; resources that are already part of the control
// plane scheme. The side-effect import of the generic control plane install is to required to
// install all the required resources in the control plane scheme.
import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/util/proto"

	kcpscheme "github.com/kcp-dev/kcp/pkg/server/scheme"
)

type schemaPuller struct {
	serverGroupsAndResources func() ([]*metav1.APIGroup, []*metav1.APIResourceList, error)
	serverPreferredResources func() ([]*metav1.APIResourceList, error)
	resourceFor              func(groupResource schema.GroupResource) (schema.GroupResource, error)
	getCRD                   func(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	models                   openapi.ModelsByGKV
}

// NewSchemaPuller allows creating a SchemaPuller from the `Config` of
// a given Kubernetes cluster, that will be able to pull API resources
// as CRDs from the given Kubernetes cluster.
func NewSchemaPuller(
	discoveryClient discovery.DiscoveryInterface,
	crdClient apiextensionsv1client.ApiextensionsV1Interface,
) (*schemaPuller, error) {
	openapiSchema, err := discoveryClient.OpenAPISchema()
	if err != nil {
		return nil, err
	}

	models, err := proto.NewOpenAPIData(openapiSchema)
	if err != nil {
		return nil, err
	}
	modelsByGKV, err := openapi.GetModelsByGKV(models)
	if err != nil {
		return nil, err
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient))

	return &schemaPuller{
		serverGroupsAndResources: discoveryClient.ServerGroupsAndResources,
		serverPreferredResources: discoveryClient.ServerPreferredResources,
		resourceFor: func(groupResource schema.GroupResource) (schema.GroupResource, error) {
			gvr, err := mapper.ResourceFor(groupResource.WithVersion(""))
			if err != nil {
				return schema.GroupResource{}, err
			}
			return gvr.GroupResource(), nil
		},
		getCRD: func(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdClient.CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		},
		models: modelsByGKV,
	}, nil
}

// PullCRDs allows pulling the resources named by their plural names
// and make them available as CRDs in the output map.
// If the list of resources is empty, it will try pulling all the resources it finds.
func (sp *schemaPuller) PullCRDs(ctx context.Context, resourceNames ...string) (map[schema.GroupResource]*apiextensionsv1.CustomResourceDefinition, error) {
	logger := klog.FromContext(ctx)

	pullAllResources := len(resourceNames) == 0
	resourcesToPull := sets.New[string]()
	for _, resourceToPull := range resourceNames {
		gr := schema.ParseGroupResource(resourceToPull)
		grToPull, err := sp.resourceFor(gr)
		if err != nil {
			logger.Error(err, "error mapping", "resource", resourceToPull)
			continue
		}
		resourcesToPull.Insert(grToPull.String())
	}

	_, apiResourcesLists, err := sp.serverGroupsAndResources()
	if err != nil {
		return nil, err
	}
	apiResourceNames := map[schema.GroupVersion]sets.Set[string]{}
	for _, apiResourcesList := range apiResourcesLists {
		gv, err := schema.ParseGroupVersion(apiResourcesList.GroupVersion)
		if err != nil {
			continue
		}

		apiResourceNames[gv] = sets.New[string]()
		for _, apiResource := range apiResourcesList.APIResources {
			apiResourceNames[gv].Insert(apiResource.Name)
		}
	}

	crds := map[schema.GroupResource]*apiextensionsv1.CustomResourceDefinition{}
	apiResourcesLists, err = sp.serverPreferredResources()
	if err != nil {
		return nil, err
	}
	for _, apiResourcesList := range apiResourcesLists {
		logger := logger.WithValues("groupVersion", apiResourcesList.GroupVersion)

		gv, err := schema.ParseGroupVersion(apiResourcesList.GroupVersion)
		if err != nil {
			logger.Error(err, "skipping discovery: error parsing")
			continue
		}

		for _, apiResource := range apiResourcesList.APIResources {
			groupResource := schema.GroupResource{
				Group:    gv.Group,
				Resource: apiResource.Name,
			}
			if !pullAllResources && !resourcesToPull.Has(groupResource.String()) {
				continue
			}

			logger := logger.WithValues("resource", apiResource.Name)

			if kcpscheme.Scheme.IsGroupRegistered(gv.Group) && !kcpscheme.Scheme.IsVersionRegistered(gv) {
				logger.Info("ignoring an apiVersion since it is part of the core KCP resources, but not compatible with KCP version")
				continue
			}

			gvk := gv.WithKind(apiResource.Kind)
			logger = logger.WithValues("kind", apiResource.Kind)
			if (kcpscheme.Scheme.Recognizes(gvk) || extensionsapiserver.Scheme.Recognizes(gvk)) && !resourcesToPull.Has(groupResource.String()) {
				logger.Info("ignoring a resource since it is part of the core KCP resources")
				continue
			}

			crdName := apiResource.Name
			if gv.Group == "" {
				crdName += ".core"
			} else {
				crdName += "." + gv.Group
			}

			var resourceScope apiextensionsv1.ResourceScope
			if apiResource.Namespaced {
				resourceScope = apiextensionsv1.NamespaceScoped
			} else {
				resourceScope = apiextensionsv1.ClusterScoped
			}

			logger = logger.WithValues("crd", crdName)
			logger.Info("processing discovery")
			var schemaProps apiextensionsv1.JSONSchemaProps
			var additionalPrinterColumns []apiextensionsv1.CustomResourceColumnDefinition
			crd, err := sp.getCRD(ctx, crdName)
			if err == nil {
				if apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.NonStructuralSchema) {
					logger.Info("non-structural schema: the resources will not be validated")
					schemaProps = apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: boolPtr(true),
					}
				} else {
					var versionFound bool
					for _, version := range crd.Spec.Versions {
						if version.Name == gv.Version {
							schemaProps = *version.Schema.OpenAPIV3Schema
							additionalPrinterColumns = version.AdditionalPrinterColumns
							versionFound = true
							break
						}
					}
					if !versionFound {
						logger.Error(nil, "expected version not found in CRD")
						schemaProps = apiextensionsv1.JSONSchemaProps{
							Type:                   "object",
							XPreserveUnknownFields: boolPtr(true),
						}
					}
				}
			} else {
				if !errors.IsNotFound(err) {
					logger.Error(err, "error looking up CRD")
					return nil, err
				}
				protoSchema := sp.models[gvk]
				if protoSchema == nil {
					logger.Info("ignoring a resource that has no OpenAPI Schema")
					continue
				}
				swaggerSpecDefinitionName := protoSchema.GetPath().String()

				var errors []error
				converter := &SchemaConverter{
					schemaProps: &schemaProps,
					schemaName:  swaggerSpecDefinitionName,
					visited:     sets.New[string](),
					errors:      &errors,
				}
				protoSchema.Accept(converter)
				if len(*converter.errors) > 0 {
					logger.Error(kerrors.NewAggregate(*converter.errors), "error during the OpenAPI schema import of resource")
					continue
				}
			}

			hasSubResource := func(subResource string) bool {
				groupResourceNames := apiResourceNames[gv]
				if groupResourceNames != nil {
					return groupResourceNames.Has(apiResource.Name + "/" + subResource)
				}
				return false
			}

			statusSubResource := &apiextensionsv1.CustomResourceSubresourceStatus{}
			if !hasSubResource("status") {
				statusSubResource = nil
			}

			scaleSubResource := &apiextensionsv1.CustomResourceSubresourceScale{
				SpecReplicasPath:   ".spec.replicas",
				StatusReplicasPath: ".status.replicas",
			}
			if !hasSubResource("scale") {
				scaleSubResource = nil
			}

			publishedCRD := &apiextensionsv1.CustomResourceDefinition{
				TypeMeta: metav1.TypeMeta{
					Kind:       "CustomResourceDefinition",
					APIVersion: "apiextensions.k8s.io/v1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:        crdName,
					Labels:      map[string]string{},
					Annotations: map[string]string{},
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: gv.Group,
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
					},
				},
			}
			if len(additionalPrinterColumns) != 0 {
				publishedCRD.Spec.Versions[0].AdditionalPrinterColumns = additionalPrinterColumns
			}
			apiextensionsv1.SetDefaults_CustomResourceDefinition(publishedCRD)

			// In Kubernetes, to make it clear to the API consumer that APIs in *.k8s.io or *.kubernetes.io domains
			// should be following all quality standards of core Kubernetes, CRDs under these domains
			// are expected to go through the API Review process and so must link the API review approval PR
			// in an `api-approved.kubernetes.io` annotation.
			// Without this annotation, a CRD under the *.k8s.io or *.kubernetes.io domains is rejected by the API server
			//
			// Of course here we're simply adding already-known resources of existing physical clusters as CRDs in KCP.
			// But to please this Kubernetes approval requirement, let's add the required annotation in imported CRDs
			// with one of the KCP PRs that hacked Kubernetes CRD support for KCP.
			if apihelpers.IsProtectedCommunityGroup(gv.Group) {
				value := "https://github.com/kcp-dev/kubernetes/pull/4"
				if crd != nil {
					if existing := crd.ObjectMeta.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; existing != "" {
						value = existing
					}
				}
				publishedCRD.ObjectMeta.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation] = value
			}
			crds[groupResource] = publishedCRD
		}
	}
	return crds, nil
}

type SchemaConverter struct {
	schemaProps *apiextensionsv1.JSONSchemaProps
	schemaName  string
	description string
	errors      *[]error
	visited     sets.Set[string]
}

func Convert(protoSchema proto.Schema, schemaProps *apiextensionsv1.JSONSchemaProps) []error {
	swaggerSpecDefinitionName := protoSchema.GetPath().String()

	var errors []error
	converter := &SchemaConverter{
		schemaProps: schemaProps,
		schemaName:  swaggerSpecDefinitionName,
		visited:     sets.New[string](),
		errors:      &errors,
	}
	protoSchema.Accept(converter)
	return *converter.errors
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
		var kind *proto.Kind
		switch subType := a.SubType.(type) {
		case *proto.Ref:
			refSchema := subType.SubSchema()
			if aKind, isKind := refSchema.(*proto.Kind); isKind {
				kind = aKind
			}
		case *proto.Kind:
			kind = subType
		}

		if val := a.Extensions["x-kubernetes-list-type"]; val != nil {
			listType := val.(string)
			sc.schemaProps.XListType = &listType
		} else if val := a.Extensions["x-kubernetes-patch-strategy"]; val != nil && val != "" {
			listType := "atomic"
			if val.(string) == "merge" || strings.HasPrefix(val.(string), "merge,") || strings.HasSuffix(val.(string), ",merge") {
				if kind != nil {
					listType = "map"
				} else {
					listType = "set"
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
	}
	subtypeSchemaProps := apiextensionsv1.JSONSchemaProps{}
	a.SubType.Accept(sc.SubConverter(&subtypeSchemaProps, a.SubType.GetDescription()))

	if len(subtypeSchemaProps.Properties) > 0 && len(sc.schemaProps.XListMapKeys) > 0 {
		required := sets.New[string](subtypeSchemaProps.Required...)
		required.Insert(sc.schemaProps.XListMapKeys...)
		for fieldName, field := range subtypeSchemaProps.Properties {
			if field.Default != nil {
				required.Delete(fieldName)
			}
		}
		subtypeSchemaProps.Required = sets.List[string](required)
	}

	sc.schemaProps.Items = &apiextensionsv1.JSONSchemaPropsOrArray{
		Schema: &subtypeSchemaProps,
	}
}
func (sc *SchemaConverter) VisitMap(m *proto.Map) {
	sc.setupDescription(m)
	subtypeSchemaProps := apiextensionsv1.JSONSchemaProps{}
	m.SubType.Accept(sc.SubConverter(&subtypeSchemaProps, m.SubType.GetDescription()))
	sc.schemaProps.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{
		Schema: &subtypeSchemaProps,
		Allows: true,
	}
	sc.schemaProps.Type = "object"
}
func (sc *SchemaConverter) VisitPrimitive(p *proto.Primitive) {
	sc.setupDescription(p)
	sc.schemaProps.Type = p.Type
	sc.schemaProps.Format = p.Format

	if defaults, ok := knownDefaults[p.Path.String()]; ok {
		sc.schemaProps.Default = defaults
	}
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
			field.Accept(sc.SubConverter(&fieldSchemaProps, field.GetDescription()))
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
	if sc.visited.Has(reference) {
		*sc.errors = append(*sc.errors, fmt.Errorf("recursive schema are not supported: %s", reference))
		return
	}
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
	sc.visited.Insert(reference)
	r.SubSchema().Accept(sc)
	sc.visited.Delete(reference)
}

func boolPtr(b bool) *bool {
	return &b
}

func (sc *SchemaConverter) SubConverter(schemaProps *apiextensionsv1.JSONSchemaProps, description string) *SchemaConverter {
	return &SchemaConverter{
		schemaProps: schemaProps,
		schemaName:  sc.schemaName,
		description: description,
		errors:      sc.errors,
		visited:     sc.visited,
	}
}

// knownPackages is a map whose content is directly borrowed from the `KnownPackages`
// map in `controller-tools `, and used to hard-code the OpenAPI V3 schema for a number
// of well-known Kubernetes types:
// https://github.com/kubernetes-sigs/controller-tools/blob/v0.5.0/pkg/crd/known_types.go#L26
var knownPackages map[string]map[string]apiextensionsv1.JSONSchemaProps = map[string]map[string]apiextensionsv1.JSONSchemaProps{
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

			// CHANGE vs the OperatorSDK code:
			// Add `x-preserve-unknown-fields: true` here since it is necessary when metadata is not at the top-level
			// (for example in Deployment spec.template.metadata), in order not to prune unknown fields and avoid
			// emptying metadata
			XPreserveUnknownFields: boolPtr(true),
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
	knownSchemas = map[string]apiextensionsv1.JSONSchemaProps{}
	for pkgName, schemas := range knownPackages {
		for typeName, schema := range schemas {
			schemaName := util.ToRESTFriendlyName(pkgName + "." + typeName)
			knownSchemas[schemaName] = schema
		}
	}
}

var knownDefaults map[string]*apiextensionsv1.JSON = map[string]*apiextensionsv1.JSON{
	"io.k8s.api.core.v1.ContainerPort.protocol": {Raw: []byte(`"TCP"`)},
	"io.k8s.api.core.v1.ServicePort.protocol":   {Raw: []byte(`"TCP"`)},
}
