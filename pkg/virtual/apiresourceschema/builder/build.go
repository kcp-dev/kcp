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

package builder

import (
	"context"
	"errors"
	"fmt"
	"path"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha1"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas"
	"github.com/kcp-dev/kcp/pkg/virtual/apiresourceschema"
	apiresourceschemaauth "github.com/kcp-dev/kcp/pkg/virtual/apiresourceschema/authorizer"
)

const (
	// controllerName is the name of the controller for post-start hooks.
	controllerName = "apiresourceschema-virtual-workspace"
)

const consumerClusterWildcard = "consumerCluster"

// pathPatterns returns the mux patterns of the virtual workspace below rootPathPrefix.
// The consumer cluster determines which APIBindings to look up.
// The clusters/<cluster> segment is for compatibility with cluster-aware clients.
func pathPatterns(rootPathPrefix string) []string {
	return []string{
		path.Join(rootPathPrefix, "{consumerCluster}"),
		path.Join(rootPathPrefix, "{consumerCluster}/clusters/{cluster}"),
	}
}

// BuildVirtualWorkspace builds the apiresourceschema virtual workspace.
// URL pattern: /services/apiresourceschema/<consumer-cluster>/clusters/*/apis/apis.kcp.io/v1alpha1/apiresourceschemas.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	cachedKcpInformers, wildcardKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	readyCh := make(chan struct{})

	klog.Background().Info("Building apiresourceschema virtual workspace", "rootPathPrefix", rootPathPrefix)

	vw := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(resolveRootPath),

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("apiresourceschema virtual workspace controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			provider := &apiDefinitionSetProvider{
				config:                  mainConfig,
				apiBindingLister:        wildcardKcpInformers.Apis().V1alpha2().APIBindings().Lister(),
				apiExportLister:         cachedKcpInformers.Apis().V1alpha2().APIExports().Lister(),
				apiResourceSchemaLister: cachedKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(),
			}

			if err := mainConfig.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"apiresourceschemas": cachedKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
					"apiexports":         cachedKcpInformers.Apis().V1alpha2().APIExports().Informer(),
					"apibindings":        wildcardKcpInformers.Apis().V1alpha2().APIBindings().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "informer not synced")
						return nil
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return provider, nil
		},
		Authorizer: newAuthorizer(kubeClusterClient),
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: apiresourceschema.VirtualWorkspaceName, Patterns: pathPatterns(rootPathPrefix), VirtualWorkspace: vw},
	}, nil
}

// resolveRootPath serves the consumer cluster's bindings.
// The cluster segment, if present, must be the wildcard or the consumer
// cluster, so a client cannot name one cluster and be routed to another.
func resolveRootPath(ctx context.Context, match framework.PathMatch) (accepted bool, prefixToStrip string, completedContext context.Context) {
	consumerPath := logicalcluster.NewPath(match.Value(consumerClusterWildcard))
	if consumerPath == logicalcluster.Wildcard {
		return false, "", ctx
	}
	consumer, ok := consumerPath.Name()
	if !ok {
		return false, "", ctx
	}

	if cluster := match.Value(framework.ClusterWildcard); cluster != "" && cluster != "*" && cluster != consumer.String() {
		return false, "", ctx
	}

	ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: consumer})
	// Use the cluster name as the API domain key for caching
	return true, match.PrefixToStrip(), dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(consumer.String()))
}

// apiDefinitionSetProvider provides APIDefinitionSets for the virtual workspace.
// It resolves APIBindings in the target cluster to find all referenced schemas.
type apiDefinitionSetProvider struct {
	config                  genericapiserver.CompletedConfig
	apiBindingLister        apisv1alpha2listers.APIBindingClusterLister
	apiExportLister         apisv1alpha2listers.APIExportClusterLister
	apiResourceSchemaLister apisv1alpha1listers.APIResourceSchemaClusterLister
}

func (p *apiDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	logger := klog.FromContext(ctx).WithValues("cluster", string(key))

	clusterName := logicalcluster.Name(key)

	// Get all APIBindings in the consumer cluster
	bindings, err := p.apiBindingLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		logger.Error(err, "failed to list APIBindings")
		return nil, false, err
	}

	logger.V(4).Info("found APIBindings", "count", len(bindings))

	if len(bindings) == 0 {
		return nil, false, nil
	}

	// Collect all schema names from all bound APIExports
	schemaClusterAndNames := make(map[string]logicalcluster.Name) // schema name -> export cluster

	for _, binding := range bindings {
		if binding.Status.Phase != apisv1alpha2.APIBindingPhaseBound {
			continue
		}

		exportClusterName := logicalcluster.Name(binding.Status.APIExportClusterName)
		if exportClusterName.Empty() {
			continue
		}

		exportName := ""
		if binding.Spec.Reference.Export != nil {
			exportName = binding.Spec.Reference.Export.Name
		}
		if exportName == "" {
			continue
		}

		// Get the APIExport
		apiExport, err := p.apiExportLister.Cluster(exportClusterName).Get(exportName)
		if err != nil {
			logger.V(4).Info("failed to get APIExport", "exportCluster", exportClusterName, "exportName", exportName, "error", err)
			continue
		}

		// Collect schema names from the export
		for _, resource := range apiExport.Spec.Resources {
			schemaClusterAndNames[resource.Schema] = exportClusterName
		}
	}

	if len(schemaClusterAndNames) == 0 {
		return nil, false, nil
	}

	logger.V(4).Info("found schemas to serve", "count", len(schemaClusterAndNames))

	// Build a REST provider that serves these schemas using the lister
	restProvider := p.createListerBasedRestProvider(schemaClusterAndNames)

	// Create the API definition for apiresourceschemas
	apiDef, err := apiserver.CreateServingInfoFor(
		p.config,
		schemas.ApisKcpDevSchemas["apiresourceschemas"],
		apisv1alpha1.SchemeGroupVersion.Version,
		restProvider,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create serving info for apiresourceschemas: %w", err)
	}

	apis = apidefinition.APIDefinitionSet{
		schema.GroupVersionResource{
			Group:    apisv1alpha1.SchemeGroupVersion.Group,
			Version:  apisv1alpha1.SchemeGroupVersion.Version,
			Resource: "apiresourceschemas",
		}: apiDef,
	}

	return apis, true, nil
}

// createListerBasedRestProvider creates a REST provider that serves schemas
// from the lister based on the schemaClusterAndNames map.
func (p *apiDefinitionSetProvider) createListerBasedRestProvider(schemaClusterAndNames map[string]logicalcluster.Name) apiserver.RestProviderFunc {
	return NewSchemaRestProvider(p.apiResourceSchemaLister, schemaClusterAndNames)
}

var _ apidefinition.APIDefinitionSetGetter = &apiDefinitionSetProvider{}

// newAuthorizer returns an authorizer that checks if the user has permission
// to read APIBindings in the consumer workspace before allowing access to schemas.
func newAuthorizer(kubeClusterClient kcpkubernetesclientset.ClusterInterface) *authorization.Decorator {
	auth := apiresourceschemaauth.NewAPIResourceSchemaAuthorizer(kubeClusterClient)
	return authorization.NewDecorator("virtual.apiresourceschema.authorization.kcp.io", auth).AddAuditLogging().AddAnonymization()
}
