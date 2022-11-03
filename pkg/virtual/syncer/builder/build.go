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
	"errors"
	"fmt"
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/client"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transformations"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/upsyncer"
)

const (
	// SyncerVirtualWorkspaceName holds the name of the virtual workspace for the syncer, used to sync resources from upstream to downstream.
	SyncerVirtualWorkspaceName string = "syncer"
	// UpsyncerVirtualWorkspaceName holds the name of the virtual workspace for the upsyncer, used to sync resources from downstream to upstream.
	UpsyncerVirtualWorkspaceName string = "upsyncer"
)

type requirementsBuilderProviderFunc func(syncTargetKey string) (labels.Requirements, error)

// BuildVirtualWorkspace builds two virtual workspaces, SyncerVirtualWorkspace and UpsyncerVirtualWorkspace by instantiating a DynamicVirtualWorkspace which,
// combined with a ForwardingREST REST storage implementation, serves a SyncTargetAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) []rootapiserver.NamedVirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	resolverProvider := func(virtualWorkspaceName string, readyCh chan struct{}) framework.RootPathResolver {
		return framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			select {
			case <-readyCh:
			default:
				return
			}

			rootPathPrefix := rootPathPrefix + virtualWorkspaceName + "/"
			completedContext = requestContext
			if !strings.HasPrefix(urlPath, rootPathPrefix) {
				return
			}
			withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

			// Incoming requests to this virtual workspace will look like:
			//  /services/(up)syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
			//                      └───────────────────────┐
			// Where the withoutRootPathPrefix starts here: ┘
			parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
			if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return
			}
			workspace := logicalcluster.New(parts[0])
			syncTargetName := parts[1]
			syncTargetUID := parts[2]

			apiDomainKey := dynamiccontext.APIDomainKey(client.ToClusterAwareKey(logicalcluster.New(parts[0]), syncTargetName))

			// In order to avoid conflicts with reusing deleted synctarget names, let's make sure that the synctarget name and synctarget UID match, if not,
			// that likely means that a syncer is running with a stale synctarget that got deleted.
			syncTarget, exists, err := wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer().GetIndexer().GetByKey(client.ToClusterAwareKey(workspace, syncTargetName))
			if !exists || err != nil {
				utilruntime.HandleError(fmt.Errorf("failed to get synctarget %s|%s: %w", workspace, syncTargetName, err))
				return
			}
			syncTargetObj := syncTarget.(*workloadv1alpha1.SyncTarget)
			if string(syncTargetObj.UID) != syncTargetUID {
				utilruntime.HandleError(fmt.Errorf("sync target UID mismatch: %s != %s", syncTargetObj.UID, syncTargetUID))
				return
			}

			realPath := "/"
			if len(parts) > 3 {
				realPath += parts[3]
			}

			//  /services/(up)syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
			//                  ┌────────────────────────────────────────────────────┘
			// We are now here: ┘
			// Now, we parse out the logical cluster.
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
			cluster := genericapirequest.Cluster{Name: logicalcluster.New(clusterName)}
			if clusterName == "*" {
				cluster.Wildcard = true
			}

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(workspace, syncTargetName)
			completedContext = genericapirequest.WithCluster(requestContext, cluster)
			completedContext = syncercontext.WithSyncTargetKey(completedContext, syncTargetKey)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
			prefixToStrip = strings.TrimSuffix(urlPath, realPath)
			accepted = true
			return
		})
	}

	// Setup the APIReconciler indexes to share between both virtualworkspaces.
	if err := wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer().AddIndexers(cache.Indexers{
		apireconciler.IndexSyncTargetsByExport: apireconciler.IndexSyncTargetsByExports,
	}); err != nil {
		return nil
	}

	if err := wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer().AddIndexers(cache.Indexers{
		apireconciler.IndexAPIExportsByAPIResourceSchema: apireconciler.IndexAPIExportsByAPIResourceSchemas,
	}); err != nil {
		return nil
	}

	requirementsBuilderProvider := func(resourceState workloadv1alpha1.ResourceState) requirementsBuilderProviderFunc {
		return func(syncTargetKey string) (labels.Requirements, error) {
			requirements, selectable := labels.SelectorFromSet(map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: string(resourceState),
			}).Requirements()
			if !selectable {
				return nil, fmt.Errorf("unable to build requirements for synctargetkey %s and resource state %s", syncTargetKey, resourceState)
			}
			return requirements, nil
		}
	}

	bootstrapManagementProvider := func(storageBuilderProvider BuildRestProviderFunc,
		virtualWorkspaceName string,
		allowedAPIFilter apireconciler.AllowedAPIfilterFunc,
		buildRequirements requirementsBuilderProviderFunc,
		transformer transforming.ResourceTransformer,
		buildStorageWrapper func(labels.Requirements) forwardingregistry.StorageWrapper, readyCh chan struct{}) func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
		return func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			apiReconciler, err := apireconciler.NewAPIReconciler(
				virtualWorkspaceName,
				kcpClusterClient,
				wildcardKcpInformers.Workload().V1alpha1().SyncTargets(),
				wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
				wildcardKcpInformers.Apis().V1alpha1().APIExports(),
				func(syncTargetWorkspace logicalcluster.Name, syncTargetName string, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, apiExportIdentityHash string) (apidefinition.APIDefinition, error) {
					syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncTargetWorkspace, syncTargetName)
					requirements, err := buildRequirements(syncTargetKey)
					if err != nil {
						return nil, err
					}
					storageWrapper := buildStorageWrapper(requirements)
					if err != nil {
						return nil, err
					}
					transformingClient := dynamicClusterClient
					if transformer != nil {
						transformingClient = transforming.WithResourceTransformer(dynamicClusterClient, transformer)
					}
					ctx, cancelFn := context.WithCancel(context.Background())
					storageBuilder := storageBuilderProvider(ctx, transformingClient, apiExportIdentityHash, storageWrapper)
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
				allowedAPIFilter,
			)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook(apireconciler.ControllerName+virtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"synctargets":        wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer(),
					"apiresourceschemas": wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
					"apiexports":         wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						klog.Errorf("informer not synced")
						return nil
					}
				}

				go apiReconciler.Start(goContext(hookContext))
				return nil
			}); err != nil {
				return nil, err
			}

			return apiReconciler, nil
		}
	}
	readyCheckerProvider := func(readyCh chan struct{}) framework.ReadyChecker {
		return framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("syncer virtual workspace controllers are not started")
			}
		})
	}

	virtualWorkspaceAuthorizer := func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		syncTargetKey := dynamiccontext.APIDomainKeyFrom(ctx)
		negotiationWorkspaceName, syncTargetName := client.SplitClusterAwareKey(string(syncTargetKey))

		authz, err := delegated.NewDelegatedAuthorizer(negotiationWorkspaceName, kubeClusterClient)
		if err != nil {
			return authorizer.DecisionNoOpinion, "Error", err
		}
		SARAttributes := authorizer.AttributesRecord{
			User:            a.GetUser(),
			Verb:            "sync",
			Name:            syncTargetName,
			APIGroup:        workloadv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      workloadv1alpha1.SchemeGroupVersion.Version,
			Resource:        "synctargets",
			ResourceRequest: true,
		}
		return authz.Authorize(ctx, SARAttributes)
	}

	transformer := &transformations.SyncerResourceTransformer{
		TransformationProvider:   &transformations.SpecDiffTransformation{},
		SummarizingRulesProvider: &transformations.DefaultSummarizingRules{},
	}

	syncerReadyCh := make(chan struct{})
	syncerVW := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver:          resolverProvider(SyncerVirtualWorkspaceName, syncerReadyCh),
		Authorizer:                authorizer.AuthorizerFunc(virtualWorkspaceAuthorizer),
		ReadyChecker:              readyCheckerProvider(syncerReadyCh),
		BootstrapAPISetManagement: bootstrapManagementProvider(NewSyncerRestProvider, SyncerVirtualWorkspaceName, nil, requirementsBuilderProvider(workloadv1alpha1.ResourceStateSync), transformer, forwardingregistry.WithStaticLabelSelector, syncerReadyCh),
	}

	upsyncerAllowedAPIFunc := func(apiGroupResource schema.GroupResource) bool {
		// Only allow persistentvolumes to be Upsynced.
		return apiGroupResource.Group == "" && apiGroupResource.Resource == "persistentvolumes"
	}

	upsyncerReadyCh := make(chan struct{})
	upsyncerVW := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver:          resolverProvider(UpsyncerVirtualWorkspaceName, upsyncerReadyCh),
		Authorizer:                authorizer.AuthorizerFunc(virtualWorkspaceAuthorizer),
		ReadyChecker:              readyCheckerProvider(upsyncerReadyCh),
		BootstrapAPISetManagement: bootstrapManagementProvider(NewUpSyncerRestProvider, UpsyncerVirtualWorkspaceName, upsyncerAllowedAPIFunc, requirementsBuilderProvider(workloadv1alpha1.ResourceStateUpsync), &upsyncer.UpsyncerResourceTransformer{}, withStaticLabelSelectorAndInWriteCallsCheck, upsyncerReadyCh),
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{
			Name:             SyncerVirtualWorkspaceName,
			VirtualWorkspace: syncerVW,
		},
		{
			Name:             UpsyncerVirtualWorkspaceName,
			VirtualWorkspace: upsyncerVW,
		},
	}
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

func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}

func withStaticLabelSelectorAndInWriteCallsCheck(labelSelector labels.Requirements) forwardingregistry.StorageWrapper {
	return func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) *forwardingregistry.StoreFuncs {

		delegateCreater := storage.CreaterFunc
		storage.CreaterFunc = func(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
			if meta, ok := obj.(metav1.Object); ok {
				if !labels.Everything().Add(labelSelector...).Matches(labels.Set(meta.GetLabels())) {
					return nil, apierrors.NewBadRequest(fmt.Sprintf("label selector %q does not match labels %v", labelSelector, meta.GetLabels()))
				}
			}
			return delegateCreater.Create(ctx, obj, createValidation, options)
		}

		delegateUpdater := storage.UpdaterFunc
		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
			obj, err := objInfo.UpdatedObject(ctx, nil)
			if apierrors.IsNotFound(err) {
				return delegateUpdater.Update(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
			}
			if err != nil {
				return nil, false, err
			}

			if meta, ok := obj.(metav1.Object); ok {
				if !labels.Everything().Add(labelSelector...).Matches(labels.Set(meta.GetLabels())) {
					return nil, false, apierrors.NewBadRequest(fmt.Sprintf("label selector %q does not match labels %v", labelSelector, meta.GetLabels()))
				}
			}
			return delegateUpdater.Update(ctx, name, objInfo, createValidation, updateValidation, forceAllowCreate, options)
		}

		staticStorage := forwardingregistry.WithStaticLabelSelector(labelSelector)

		return staticStorage(resource, storage)
	}
}
