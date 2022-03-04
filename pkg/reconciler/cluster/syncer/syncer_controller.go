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

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	workloadinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

type Controller struct {
	name              string
	crdClusterClient  *apiextensionsclient.Cluster
	syncerManager     *syncerManager
	clusterReconciler *cluster.ClusterReconciler
}

func NewController(
	crdClusterClient *apiextensionsclient.Cluster,
	kcpClusterClient *kcpclient.Cluster,
	clusterInformer workloadinformer.WorkloadClusterInformer,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
	kubeconfig clientcmdapi.Config,
	resourcesToSync []string,
	syncerManagerImpl syncerManagerImpl,
) (*Controller, error) {

	sm := &syncerManager{
		name:                     syncerManagerImpl.name(),
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
		name:              syncerManagerImpl.name(),
		crdClusterClient:  crdClusterClient,
		syncerManager:     sm,
		clusterReconciler: cr,
	}, nil
}

// TODO(sttts): fix the many races due to unprotected field access and then increase worker count
func (c *Controller) Start(ctx context.Context) {
	c.clusterReconciler.Start(ctx)
}
