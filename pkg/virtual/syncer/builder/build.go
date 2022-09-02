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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers/apireconciler"
)

const SyncerVirtualWorkspaceName string = "syncer"

// BuildVirtualWorkspace builds a SyncerVirtualWorkspace by instanciating a DynamicVirtualWorkspace which, combined with a
// ForwardingREST REST storage implementation, serves a SyncTargetAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kubernetesclient.ClusterInterface,
	dynamicClusterClient dynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			select {
			case <-readyCh:
			default:
				return
			}

			completedContext = requestContext
			if !strings.HasPrefix(urlPath, rootPathPrefix) {
				return
			}
			withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

			// Incoming requests to this virtual workspace will look like:
			//  /services/syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
			//                  └───────────────────────────┐
			// Where the withoutRootPathPrefix starts here: ┘
			parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
			if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return
			}
			workspace := parts[0]
			workloadCusterName := parts[1]
			syncTargetUID := parts[2]
			apiDomainKey := dynamiccontext.APIDomainKey(clusters.ToClusterAwareKey(logicalcluster.New(parts[0]), workloadCusterName))

			// In order to avoid conflicts with reusing deleted synctarget names, let's make sure that the synctarget name and synctarget UID match, if not,
			// that likely means that a syncer is running with a stale synctarget that got deleted.
			syncTarget, exists, err := wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer().GetIndexer().GetByKey(clusters.ToClusterAwareKey(logicalcluster.New(workspace), workloadCusterName))
			if !exists || err != nil {
				runtime.HandleError(fmt.Errorf("failed to get synctarget %s|%s: %w", workspace, workloadCusterName, err))
				return
			}
			syncTargetObj := syncTarget.(*workloadv1alpha1.SyncTarget)
			if string(syncTargetObj.UID) != syncTargetUID {
				runtime.HandleError(fmt.Errorf("sync target UID mismatch: %s != %s", syncTargetObj.UID, syncTargetUID))
				return
			}

			realPath := "/"
			if len(parts) > 3 {
				realPath += parts[3]
			}

			//  /services/syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
			//                  ┌───────────────────────────────────────────────┘
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

			completedContext = genericapirequest.WithCluster(requestContext, cluster)
			completedContext = syncercontext.WithSyncTargetName(completedContext, workloadCusterName)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
			prefixToStrip = strings.TrimSuffix(urlPath, realPath)
			accepted = true
			return
		}),
		Authorizer: authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
			syncTargetKey := dynamiccontext.APIDomainKeyFrom(ctx)
			negotiationWorkspaceName, syncTargetName := clusters.SplitClusterAwareKey(string(syncTargetKey))

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
		}),
		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("syncer virtual workspace controllers are not started")
			}
		}),
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			apiReconciler, err := apireconciler.NewAPIReconciler(
				kcpClusterClient,
				wildcardKcpInformers.Workload().V1alpha1().SyncTargets(),
				wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
				wildcardKcpInformers.Apis().V1alpha1().APIExports(),
				func(syncTargetWorkspace logicalcluster.Name, syncTargetName string, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, apiExportIdentityHash string) (apidefinition.APIDefinition, error) {
					syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncTargetWorkspace, syncTargetName)
					requirements, selectable := labels.SelectorFromSet(map[string]string{
						workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: string(workloadv1alpha1.ResourceStateSync),
					}).Requirements()
					if !selectable {
						return nil, fmt.Errorf("unable to create a selector from the provided labels")
					}
					storageWrapper := forwardingregistry.WithStaticLabelSelector(requirements)

					ctx, cancelFn := context.WithCancel(context.Background())
					storageBuilder := NewStorageBuilder(ctx, dynamicClusterClient, apiExportIdentityHash, storageWrapper)
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
			)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook(apireconciler.ControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
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
