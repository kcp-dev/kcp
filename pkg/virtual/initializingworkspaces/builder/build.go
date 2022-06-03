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

package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	crdlisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/reservedcrdgroups"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
}

func BuildVirtualWorkspace(
	rootPathPrefix string,
	dynamicClusterClient dynamic.ClusterInterface,
	wildcardApiExtensionsInformers apiextensionsinformers.SharedInformerFactory,
) framework.VirtualWorkspace {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		Name: initializingworkspaces.VirtualWorkspaceName,
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {

			completedContext = requestContext
			if !strings.HasPrefix(urlPath, rootPathPrefix) {
				return
			}
			withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

			// Incoming requests to this virtual workspace will look like:
			//  /services/initializingworkspaces/<workspace>/<initializer>/clusters/*/apis/workload.kcp.dev/v1alpha1/workloadclusters
			//                                  └───────────┐
			// Where the withoutRootPathPrefix starts here: ┘
			parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
			if len(parts) < 3 {
				return
			}

			initializerWorkspace := parts[0]
			if initializerWorkspace == "" {
				return
			}

			initializerName := parts[1]
			if initializerName == "" {
				return
			}

			realPath := "/" + parts[2]

			//  /services/initializingworkspaces/<workspace>/<initializer>/clusters/*/apis/workload.kcp.dev/v1alpha1/workloadclusters
			//                  ┌─────────────────────────────────────────┘
			// We are now here: ┘
			// Now, we parse out the logical cluster and validate that only wildcard requests are allowed.
			if !strings.HasPrefix(realPath, "/clusters/") {
				return // don't accept
			}

			withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
			parts = strings.SplitN(withoutClustersPrefix, "/", 2)
			clusterName := parts[0]
			realPath = "/"
			if len(parts) > 1 {
				realPath += parts[1]
			}

			if clusterName != "*" {
				return false, "", nil
				// TODO: how do we signal that this is an error?
			}

			completedContext = genericapirequest.WithCluster(requestContext, genericapirequest.Cluster{Name: logicalcluster.Wildcard, Wildcard: true})
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, dynamiccontext.APIDomainKey(fmt.Sprintf("%s|%s", initializerWorkspace, initializerName)))
			prefixToStrip = strings.TrimSuffix(urlPath, realPath)
			accepted = true
			return
		},
		Authorizer: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
			klog.Error("the authorizer for the 'initializingworkspaces' virtual workspace is not implemented !")
			return authorizer.DecisionAllow, "", nil
		},
		Ready: func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("initializingworkspaces virtual workspace controllers are not started")
			}
		},
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			retriever := &apiSetRetriever{
				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				crdLister:            wildcardApiExtensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Lister(),
			}

			if err := mainConfig.AddPostStartHook(initializingworkspaces.VirtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"customresourcedefinitions": wildcardApiExtensionsInformers.Apiextensions().V1().CustomResourceDefinitions().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return retriever, nil
		},
	}
}

type apiSetRetriever struct {
	config               genericapiserver.CompletedConfig
	dynamicClusterClient dynamic.ClusterInterface
	crdLister            crdlisters.CustomResourceDefinitionLister
}

func (a *apiSetRetriever) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	parts := strings.Split(string(key), "|")
	if len(parts) != 2 {
		return nil, false, fmt.Errorf("invalid API domain key, expected workspace|initializerName: %q", key)
	}
	initializerWorkspace, initializerName := parts[0], parts[1]

	crd, err := a.crdLister.Get(
		clusters.ToClusterAwareKey(
			logicalcluster.New(reservedcrdgroups.SystemCRDLogicalClusterName),
			"clusterworkspaces.tenancy.kcp.dev",
		),
	)
	if err != nil {
		return nil, false, err
	}

	apiResourceSchema := &apisv1alpha1.APIResourceSchema{
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: crd.Spec.Group,
			Names: crd.Spec.Names,
			Scope: crd.Spec.Scope,
		},
	}

	for i := range crd.Spec.Versions {
		crdVersion := crd.Spec.Versions[i]

		apiResourceVersion := apisv1alpha1.APIResourceVersion{
			Name:                     crdVersion.Name,
			Served:                   crdVersion.Served,
			Storage:                  crdVersion.Storage,
			Deprecated:               crdVersion.Deprecated,
			DeprecationWarning:       crdVersion.DeprecationWarning,
			AdditionalPrinterColumns: crdVersion.AdditionalPrinterColumns,
		}

		if crdVersion.Schema != nil && crdVersion.Schema.OpenAPIV3Schema != nil {
			schemaBytes, err := json.Marshal(crdVersion.Schema.OpenAPIV3Schema)
			if err != nil {
				return nil, false, fmt.Errorf("error converting version %q schema: %w", crdVersion.Name, err)
			}
			apiResourceVersion.Schema.Raw = schemaBytes
		}

		// we never expose subresources for this virtual workspace

		apiResourceSchema.Spec.Versions = append(apiResourceSchema.Spec.Versions, apiResourceVersion)
	}

	apis = make(apidefinition.APIDefinitionSet, len(apiResourceSchema.Spec.Versions))
	for _, version := range apiResourceSchema.Spec.Versions {
		gvr := schema.GroupVersionResource{
			Group:    apiResourceSchema.Spec.Group,
			Version:  version.Name,
			Resource: apiResourceSchema.Spec.Names.Plural,
		}
		apiDefinition, err := apiserver.CreateServingInfoFor(
			a.config,
			apiResourceSchema,
			version.Name,
			provideForwardingRestStorage(ctx, a.dynamicClusterClient, initializerWorkspace, initializerName),
		)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create serving info: %w", err)
		}
		apis[gvr] = apiDefinition
	}

	return apis, len(apis) > 0, nil
}

var _ apidefinition.APIDefinitionSetGetter = &apiSetRetriever{}
