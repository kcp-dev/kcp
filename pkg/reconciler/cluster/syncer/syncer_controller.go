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

package syncer

import (
	"context"
	"strings"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	clusterinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

type Controller struct {
	name                string
	apiExtensionsClient apiextensionsclient.Interface
	syncerManager       *syncerManager
	clusterReconciler   *cluster.ClusterReconciler
}

func NewController(
	apiExtensionsClient apiextensionsclient.Interface,
	kcpClusterClient *kcpclient.Cluster,
	clusterInformer clusterinformer.ClusterInformer,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
	kubeconfig clientcmdapi.Config,
	resourcesToSync []string,
	syncerManagerImpl syncerManagerImpl,
) (*Controller, error) {

	sm := &syncerManager{
		name:                     syncerManagerImpl.name(),
		apiExtensionsClient:      apiExtensionsClient,
		kubeconfig:               kubeconfig,
		resourcesToSync:          resourcesToSync,
		syncerManagerImpl:        syncerManagerImpl,
		apiresourceImportIndexer: apiResourceImportInformer.Informer().GetIndexer(),
	}

	cr, err := clusterctl.NewClusterReconciler(
		syncerManagerImpl.name(),
		sm,
		kcpClusterClient,
		clusterInformer,
		apiResourceImportInformer,
	)
	if err != nil {
		return nil, err
	}

	return &Controller{
		name:                syncerManagerImpl.name(),
		apiExtensionsClient: apiExtensionsClient,
		syncerManager:       sm,
		clusterReconciler:   cr,
	}, nil
}

type preparedController struct {
	Controller
}

type PreparedController struct {
	*preparedController
}

func (c *Controller) Prepare() (PreparedController, error) {
	// TODO(ncdc): does this need a per-cluster client?
	// TODO(sttts): make resilient to errors
	discoveryClient := c.apiExtensionsClient.Discovery()
	serverGroups, err := discoveryClient.ServerGroups()
	if err != nil {
		return PreparedController{}, err
	}
	for _, apiGroup := range serverGroups.Groups {
		if genericcontrolplanescheme.Scheme.IsGroupRegistered(apiGroup.Name) {
			for _, version := range apiGroup.Versions {
				gv := schema.GroupVersion{
					Group:   apiGroup.Name,
					Version: version.Version,
				}
				if genericcontrolplanescheme.Scheme.IsVersionRegistered(gv) {
					apiResourceList, err := discoveryClient.ServerResourcesForGroupVersion(gv.String())
					if err != nil {
						return PreparedController{}, err
					}
					for _, apiResource := range apiResourceList.APIResources {
						gvk := gv.WithKind(apiResource.Kind)
						if !strings.Contains(apiResource.Name, "/") && genericcontrolplanescheme.Scheme.Recognizes(gvk) {
							c.syncerManager.genericControlPlaneResources = append(c.syncerManager.genericControlPlaneResources, gv.WithResource(apiResource.Name))
						}
					}
				}
			}
		}
	}

	return PreparedController{&preparedController{
		Controller: *c,
	}}, nil
}

// TODO(sttts): fix the many races due to unprotected field access and then increase worker count
func (c *PreparedController) Start(ctx context.Context) {
	c.clusterReconciler.Start(ctx)
}
