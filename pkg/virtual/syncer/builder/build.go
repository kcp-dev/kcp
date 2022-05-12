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
	"strings"

	"github.com/kcp-dev/logicalcluster"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers"
)

const SyncerVirtualWorkspaceName string = "syncer"

// BuildVirtualWorkspace builds a SyncerVirtualWorkspace by instanciating a DynamicVirtualWorkspace which, combined with a
// ForwardingREST REST storage implementation, serves a WorkloadClusterAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(rootPathPrefix string, dynamicClusterClient dynamic.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, wildcardKcpInformers kcpinformer.SharedInformerFactory) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	var installedAPIs *installedAPIs
	ready := false

	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		Name: SyncerVirtualWorkspaceName,
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			if installedAPIs == nil {
				return
			}
			completedContext = requestContext
			if path := urlPath; strings.HasPrefix(path, rootPathPrefix) {
				withoutRootPathPrefix := strings.TrimPrefix(path, rootPathPrefix)
				parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
				if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
					return
				}
				workloadClusterKey := syncer.WorkloadClusterRef{
					LogicalClusterName: parts[0],
					Name:               parts[1],
				}.Key()

				realPath := "/"
				if len(parts) > 2 {
					realPath += parts[2]
				}

				cluster := genericapirequest.Cluster{Name: logicalcluster.Wildcard, Wildcard: true}
				if strings.HasPrefix(realPath, "/clusters/") {
					withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
					parts = strings.SplitN(withoutClustersPrefix, "/", 2)
					lclusterName := parts[0]
					realPath = "/"
					if len(parts) > 1 {
						realPath += parts[1]
					}
					cluster = genericapirequest.Cluster{Name: logicalcluster.New(lclusterName)}
					if lclusterName == "*" {
						cluster.Wildcard = true
					}
				}
				completedContext = genericapirequest.WithCluster(requestContext, cluster)

				if _, exists := installedAPIs.GetAPIs(workloadClusterKey); !exists {
					return
				}

				completedContext = context.WithValue(completedContext, apidefs.APIDomainKeyContextKey, workloadClusterKey)
				prefixToStrip = strings.TrimSuffix(path, realPath)
				accepted = true
				return
			}
			return
		},
		Ready: func() error {
			if !ready {
				return errors.New("syncer virtual workspace controllers are not started")
			}
			return nil
		},
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefs.APISetRetriever, error) {

			clusterInformer := wildcardKcpInformers.Workload().V1alpha1().WorkloadClusters().Informer()
			apiResourceImportInformer := wildcardKcpInformers.Apiresource().V1alpha1().APIResourceImports()
			negotiatedAPIResourceInformer := wildcardKcpInformers.Apiresource().V1alpha1().NegotiatedAPIResources()

			installedAPIs = newInstalledAPIs(func(logicalClusterName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (apidefs.APIDefinition, error) {
				return apiserver.CreateServingInfoFor(mainConfig, logicalClusterName, spec, provideForwardingRestStorage(&clusterAwareClientGetter{
					clusterInterface: dynamicClusterClient,
				}))
			})

			// This should be replaced by a real controller when the URLs should be added to the WorkloadCluster object
			clusterInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					if workloadCluster, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						workloadClusterRef := syncer.WorkloadClusterRef{
							LogicalClusterName: workloadCluster.ClusterName,
							Name:               workloadCluster.Name,
						}
						installedAPIs.addWorkloadCluster(workloadClusterRef)
						for _, api := range internalAPIs {
							_ = installedAPIs.Upsert(syncer.WorkloadClusterAPI{
								WorkloadClusterRef: workloadClusterRef,
								Spec:               api,
							})
						}
					}
				},
				DeleteFunc: func(obj interface{}) {
					if workloadCluster, ok := obj.(*workloadv1alpha1.WorkloadCluster); ok {
						installedAPIs.removeWorkloadCluster(syncer.WorkloadClusterRef{
							LogicalClusterName: workloadCluster.ClusterName,
							Name:               workloadCluster.Name,
						})
					}
				},
			})

			apiReconciler, err := controllers.NewAPIReconciler(installedAPIs, kcpClusterClient, apiResourceImportInformer, negotiatedAPIResourceInformer)
			if err != nil {
				return nil, err
			}

			if err := mainConfig.AddPostStartHook("apiresourceimports.kcp.dev-api-reconciler", func(hookContext genericapiserver.PostStartHookContext) error {
				for name, informer := range map[string]cache.SharedIndexInformer{
					"apiresourceimports":     apiResourceImportInformer.Informer(),
					"negotiatedapiresources": negotiatedAPIResourceInformer.Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
					}
				}

				ready = true
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					<-hookContext.StopCh
					cancel()
				}()
				go apiReconciler.Start(ctx)
				return nil
			}); err != nil {
				return nil, err
			}

			return installedAPIs, nil
		},
	}
}
