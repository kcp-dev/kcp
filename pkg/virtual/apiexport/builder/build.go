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

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/registry/rbac/validation"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	aeadmission "github.com/kcp-dev/kcp/pkg/virtual/apiexport/admission"
	virtualapiexportauth "github.com/kcp-dev/kcp/pkg/virtual/apiexport/authorizer"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

const (
	// VirtualWorkspaceName is the name of the virtual workspace.
	VirtualWorkspaceName string = "apiexport"
	// OriginalUserAnnotationKey is the key used in a user's "extra" to
	// specify the original user of the authenticating request.
	OriginalUserAnnotationKey = "experimental.authorization.kcp.io/original-username"
	// OriginalGroupsAnnotationKey is the key used in a user's "extra" to
	// specify the original groups of the authenticating request.
	OriginalGroupsAnnotationKey = "experimental.authorization.kcp.io/original-groups"
)

func BuildVirtualWorkspace(
	rootPathPrefix string,
	cfg *rest.Config,
	kubeClusterClient, deepSARClient kcpkubernetesclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	cachedKcpInformers, kcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	apiExportAdmission := aeadmission.NewSelectorAdmission(kcpInformers.Apis().V1alpha2().APIBindings(), kubeClusterClient)

	boundOrClaimedWorkspaceContent := &virtualdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("apiexport virtual workspace controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			dynamicClient, err := kcpdynamic.NewForConfig(cfg)
			if err != nil {
				return nil, fmt.Errorf("error creating privileged dynamic kcp client: %w", err)
			}

			impersonatedDynamicClientGetter := func(ctx context.Context) (kcpdynamic.ClusterInterface, error) {
				cluster, err := genericapirequest.ValidClusterFrom(ctx)
				if err != nil {
					return nil, fmt.Errorf("error getting valid cluster from context: %w", err)
				}

				user, found := genericapirequest.UserFrom(ctx)
				if !found {
					return nil, fmt.Errorf("error getting user from context")
				}

				// Wildcard requests cannot be impersonated against a concrete cluster.
				if cluster.Wildcard {
					return dynamicClient, nil
				}

				// Add a warrant of a fake local service account giving full access
				warrant := validation.Warrant{
					User:   "system:serviceaccount:default:rest",
					Groups: []string{bootstrap.SystemKcpAdminGroup},
					Extra: map[string][]string{
						serviceaccount.ClusterNameKey: {cluster.Name.Path().String()},
					},
				}

				bs, err := json.Marshal(warrant)
				if err != nil {
					return nil, fmt.Errorf("error marshaling warrant: %w", err)
				}

				// Impersonate the request user and add the warrant as an extra
				impersonationConfig := rest.CopyConfig(cfg)
				impersonationConfig.Impersonate = rest.ImpersonationConfig{
					UserName: user.GetName(),
					Groups:   user.GetGroups(),
					UID:      user.GetUID(),
					Extra:    user.GetExtra(),
				}
				if impersonationConfig.Impersonate.Extra == nil {
					impersonationConfig.Impersonate.Extra = map[string][]string{}
				}
				impersonationConfig.Impersonate.Extra[validation.WarrantExtraKey] = append(impersonationConfig.Impersonate.Extra[validation.WarrantExtraKey], string(bs))
				impersonatedClient, err := kcpdynamic.NewForConfig(impersonationConfig)
				if err != nil {
					return nil, fmt.Errorf("error generating dynamic client: %w", err)
				}
				return impersonatedClient, nil
			}

			apiReconciler, err := apireconciler.NewAPIReconciler(
				kcpClusterClient,
				cachedKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
				cachedKcpInformers.Apis().V1alpha2().APIExports(),
				func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string, optionalLabelRequirements labels.Requirements) (apidefinition.APIDefinition, error) {
					ctx, cancelFn := context.WithCancel(context.Background())

					var wrapper forwardingregistry.StorageWrapper
					if len(optionalLabelRequirements) > 0 {
						wrapper = forwardingregistry.WithLabelSelector(func(_ context.Context) labels.Requirements {
							return optionalLabelRequirements
						})
					}

					storageBuilder := provideDelegatingRestStorage(ctx, impersonatedDynamicClientGetter, identityHash, wrapper)
					def, err := apiserver.CreateServingInfoFor(mainConfig, apiResourceSchema, version, storageBuilder)
					if err != nil {
						cancelFn()
						return nil, err
					}
					return &apiDefinitionWithCancel{
						APIDefinition: def,
						cancelFn:      cancelFn,
					}, nil
				},
				func(ctx context.Context, apibindingVersion string, clusterName logicalcluster.Name, apiExportName string) (apidefinition.APIDefinition, error) {
					restProvider, err := provideAPIExportFilteredRestStorage(ctx, impersonatedDynamicClientGetter, clusterName, apiExportName)
					if err != nil {
						return nil, err
					}

					return apiserver.CreateServingInfoFor(
						mainConfig,
						schemas.ApisKcpDevSchemas["apibindings"],
						apibindingVersion,
						restProvider,
					)
				},
			)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook(apireconciler.ControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"apiresourceschemas": cachedKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
					"apiexports":         cachedKcpInformers.Apis().V1alpha2().APIExports().Informer(),
					"apibindings":        kcpInformers.Apis().V1alpha2().APIBindings().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "informer not synced")
						return nil
					}
				}

				go apiReconciler.Start(hookContext)
				return nil
			}); err != nil {
				return nil, err
			}

			return apiReconciler, nil
		},
		Authorizer: newAuthorizer(kubeClusterClient, deepSARClient, cachedKcpInformers, kcpInformers),
		Mutator:    apiExportAdmission,
		Validator:  apiExportAdmission,
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: VirtualWorkspaceName, VirtualWorkspace: boundOrClaimedWorkspaceContent},
	}, nil
}

func digestUrl(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     └────────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	apiExportClusterName, apiExportName := parts[0], parts[1]
	if apiExportClusterName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}
	if apiExportName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 2 {
		realPath += parts[2]
	}

	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     ┌────────────────────────────┘
	// We are now here: ───┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	path := logicalcluster.NewPath(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	cluster = genericapirequest.Cluster{}
	if path == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		var ok bool
		cluster.Name, ok = path.Name()
		if !ok {
			return genericapirequest.Cluster{}, "", "", false
		}
	}

	key := fmt.Sprintf("%s/%s", apiExportClusterName, apiExportName)
	return cluster, dynamiccontext.APIDomainKey(key), strings.TrimSuffix(urlPath, realPath), true
}

func newAuthorizer(kubeClusterClient, deepSARClient kcpkubernetesclientset.ClusterInterface, cachedKcpInformers, kcpInformers kcpinformers.SharedInformerFactory) authorizer.Authorizer {
	maximalPermissionAuth := virtualapiexportauth.NewMaximalPermissionAuthorizer(deepSARClient, cachedKcpInformers.Apis().V1alpha2().APIExports())
	maximalPermissionAuth = authorization.NewDecorator("virtual.apiexport.maxpermissionpolicy.authorization.kcp.io", maximalPermissionAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	apiExportsContentAuth := virtualapiexportauth.NewAPIExportsContentAuthorizer(maximalPermissionAuth, kubeClusterClient)
	apiExportsContentAuth = authorization.NewDecorator("virtual.apiexport.content.authorization.kcp.io", apiExportsContentAuth).AddAuditLogging().AddAnonymization()

	boundApiAuth := virtualapiexportauth.NewBoundAPIAuthorizer(apiExportsContentAuth, kcpInformers.Apis().V1alpha2().APIBindings(), cachedKcpInformers.Apis().V1alpha2().APIExports(), kubeClusterClient)
	boundApiAuth = authorization.NewDecorator("virtual.apiexport.boundapi.authorization.kcp.io", boundApiAuth).AddAuditLogging().AddAnonymization()

	return boundApiAuth
}

// apiDefinitionWithCancel calls the cancelFn on tear-down.
type apiDefinitionWithCancel struct {
	apidefinition.APIDefinition
	cancelFn func()
}

func (d *apiDefinitionWithCancel) TearDown() {
	d.cancelFn()
	d.APIDefinition.TearDown()
}
