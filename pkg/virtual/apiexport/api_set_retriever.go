package apiexport

import (
	"strings"

	"github.com/kcp-dev/logicalcluster"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

type apiSetRetriever struct {
	genericConfig genericapiserver.CompletedConfig

	dynamicClusterClient dynamic.ClusterInterface

	getAPIExport         func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func (a *apiSetRetriever) GetAPIDefinitionSet(key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool) {
	parts := strings.Split(string(key), "/")
	if len(parts) != 3 {
		return
	}

	apiExportClusterName, apiExportName, identityHash := parts[0], parts[1], parts[2]
	if apiExportClusterName == "" {
		return
	}
	if apiExportName == "" {
		return
	}
	if identityHash == "" {
		return
	}

	clusterName := logicalcluster.New(apiExportClusterName)
	apiExport, err := a.getAPIExport(clusterName, apiExportName)
	if err != nil {
		// TODO(ncdc): would be nice to expose some sort of user-visible error
		return
	}

	if apiExport.Status.IdentityHash != identityHash {
		// TODO(ncdc): is this right?
		// TODO(ncdc): would be nice to expose some sort of user-visible error
		return
	}

	apis = map[schema.GroupVersionResource]apidefinition.APIDefinition{}

	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiResourceSchema, err := a.getAPIResourceSchema(clusterName, schemaName)
		if err != nil {
			// TODO(ncdc): would be nice to expose some sort of user-visible error
			continue
		}

		for _, version := range apiResourceSchema.Spec.Versions {
			gvr := schema.GroupVersionResource{
				Group:    apiResourceSchema.Spec.Group,
				Version:  version.Name,
				Resource: apiResourceSchema.Spec.Names.Plural,
			}

			var subresources v1alpha1.SubResources
			if version.Subresources.Status != nil {
				subresources = append(subresources, v1alpha1.SubResource{Name: "status"})
			}
			if version.Subresources.Scale != nil {
				subresources = append(subresources, v1alpha1.SubResource{Name: "scale"})
			}

			var columnDefinitions v1alpha1.ColumnDefinitions
			columnDefinitions.ImportFromCRDAdditionalPrinterColumns(version.AdditionalPrinterColumns)

			apiResourceSpec := &v1alpha1.CommonAPIResourceSpec{
				GroupVersion: v1alpha1.GroupVersion{
					Group:   gvr.Group,
					Version: gvr.Version,
				},
				Scope:                         apiResourceSchema.Spec.Scope,
				CustomResourceDefinitionNames: apiResourceSchema.Spec.Names,
				OpenAPIV3Schema:               version.Schema,
				SubResources:                  subresources,
				ColumnDefinitions:             columnDefinitions,
			}

			apiDefinition, err := apiserver.CreateServingInfoFor(
				a.genericConfig,
				clusterName,
				apiResourceSpec,
				provideForwardingRestStorage(a.dynamicClusterClient, identityHash),
			)
			if err != nil {
				// TODO(ncdc): would be nice to expose some sort of user-visible error
				continue
			}

			apis[gvr] = apiDefinition
		}
	}

	return apis, len(apis) > 0
}

func provideForwardingRestStorage(clusterClient dynamic.ClusterInterface, identityHash string) apiserver.RestProviderFunc {
	return func(
		resource schema.GroupVersionResource,
		kind schema.GroupVersionKind,
		listKind schema.GroupVersionKind,
		typer runtime.ObjectTyper,
		tableConvertor rest.TableConvertor,
		namespaceScoped bool,
		schemaValidator *validate.SchemaValidator,
		subresourcesSchemaValidator map[string]*validate.SchemaValidator,
		structuralSchema *structuralschema.Structural,
	) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		resource.Resource += ":" + identityHash

		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		var scaleSpec *apiextensions.CustomResourceSubresourceScale
		// TODO(sttts): implement scale subresource

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			schemaValidator,
			statusSchemaValidate,
			map[string]*structuralschema.Structural{resource.Version: structuralSchema},
			statusSpec,
			scaleSpec,
		)

		storage := registry.NewStorage(
			resource,
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			clusterClient,
			nil,
		)

		subresourceStorages = make(map[string]rest.Storage)
		if statusEnabled {
			subresourceStorages["status"] = storage.Status
		}

		// TODO(sttts): add scale subresource

		return storage.CustomResource, subresourceStorages
	}
}
