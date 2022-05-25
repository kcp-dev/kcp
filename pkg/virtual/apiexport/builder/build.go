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

	"github.com/kcp-dev/logicalcluster"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const VirtualWorkspaceName string = "apiexport"

func BuildVirtualWorkspace(
	rootPathPrefix string,
	dynamicClusterClient dynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) framework.VirtualWorkspace {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	return &virtualdynamic.DynamicVirtualWorkspace{
		Name: VirtualWorkspaceName,

		RootPathResolver: func(path string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext

			if !strings.HasPrefix(path, rootPathPrefix) {
				return
			}

			// Incoming requests to this virtual workspace will look like:
			//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
			//                     └────────────────────────┐
			// Where the withoutRootPathPrefix starts here: ┘
			withoutRootPathPrefix := strings.TrimPrefix(path, rootPathPrefix)

			parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
			if len(parts) < 3 {
				return
			}

			apiExportClusterName, apiExportName := parts[0], parts[1]
			if apiExportClusterName == "" {
				return
			}
			if apiExportName == "" {
				return
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
			key := fmt.Sprintf("%s/%s", apiExportClusterName, apiExportName)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, dynamiccontext.APIDomainKey(key))

			prefixToStrip = strings.TrimSuffix(path, realPath)
			accepted = true

			return
		},

		Ready: func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("apiexport virtual workspace controllers are not started")
			}
		},

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			apiReconciler, err := apireconciler.NewAPIReconciler(
				kcpClusterClient,
				wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
				wildcardKcpInformers.Apis().V1alpha1().APIExports(),
				func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, apiExportIdentityHash string) (apidefinition.APIDefinition, error) {
					ctx, cancelFn := context.WithCancel(context.Background())
					storageBuilder := NewStorageBuilder(ctx, dynamicClusterClient, apiExportIdentityHash, nil)
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
					"apiresourceschemas": wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
					"apiexports":         wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer(),
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
