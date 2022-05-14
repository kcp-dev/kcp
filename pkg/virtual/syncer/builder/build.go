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
	"k8s.io/client-go/tools/clusters"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers/apireconciler"
)

const SyncerVirtualWorkspaceName string = "syncer"

// BuildVirtualWorkspace builds a SyncerVirtualWorkspace by instanciating a DynamicVirtualWorkspace which, combined with a
// ForwardingREST REST storage implementation, serves a WorkloadClusterAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(rootPathPrefix string, dynamicClusterClient dynamic.ClusterInterface, kcpClusterClient kcpclient.ClusterInterface, wildcardKcpInformers kcpinformer.SharedInformerFactory) framework.VirtualWorkspace {

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		Name: SyncerVirtualWorkspaceName,
		RootPathResolver: func(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
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

			// paths like: .../root:org:ws/<workload-cluster-name>/clusters/*/api/v1/configmaps
			parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
			if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
				return
			}
			apiDomainKey := dynamiccontext.APIDomainKey(clusters.ToClusterAwareKey(logicalcluster.New(parts[0]), parts[1]))

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
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
			prefixToStrip = strings.TrimSuffix(urlPath, realPath)
			accepted = true
			return
		},
		Ready: func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("syncer virtual workspace controllers are not started")
			}
		},
		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			apiReconciler, err := apireconciler.NewAPIReconciler(
				kcpClusterClient,
				wildcardKcpInformers.Workload().V1alpha1().WorkloadClusters(),
				wildcardKcpInformers.Apiresource().V1alpha1().NegotiatedAPIResources(),
				func(logicalClusterName logicalcluster.Name, workloadClusterName string, spec *apiresourcev1alpha1.CommonAPIResourceSpec) (apidefinition.APIDefinition, error) {
					ctx, cancelFn := context.WithCancel(context.Background())
					def, err := apiserver.CreateServingInfoFor(mainConfig, logicalClusterName, spec, provideForwardingRestStorage(ctx, dynamicClusterClient, workloadClusterName))
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

			if err := mainConfig.AddPostStartHook("apiresourceimports.kcp.dev-api-reconciler", func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				for name, informer := range map[string]cache.SharedIndexInformer{
					"workloadclusters":       wildcardKcpInformers.Workload().V1alpha1().WorkloadClusters().Informer(),
					"negotiatedapiresources": wildcardKcpInformers.Apiresource().V1alpha1().NegotiatedAPIResources().Informer(),
				} {
					if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
						return errors.New("informer not synced")
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
