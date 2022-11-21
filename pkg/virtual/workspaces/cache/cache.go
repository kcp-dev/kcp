/*
Copyright 2021 The KCP Authors.

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

package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// NewClusterWorkspaceCache returns a wrapper around an informer. It serves from the informer, and on cache-miss
// it looks up through the given client.
func NewClusterWorkspaceCache(workspaces tenancyv1alpha1informers.ClusterWorkspaceClusterInformer, kcpClusterClient kcpclientset.ClusterInterface) *ClusterWorkspaceCache {
	return &ClusterWorkspaceCache{
		kcpClusterClient: kcpClusterClient,
		lister:           workspaces.Lister(),
		HasSynced:        workspaces.Informer().HasSynced,
	}
}

type ClusterWorkspaceCache struct {
	kcpClusterClient kcpclientset.ClusterInterface
	lister           tenancyv1alpha1listers.ClusterWorkspaceClusterLister
	HasSynced        cache.InformerSynced
}

func (c *ClusterWorkspaceCache) Get(clusterName logicalcluster.Name, workspaceName string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	// check for cluster workspace in the cache
	clusterWorkspace, err := c.lister.Cluster(clusterName).Get(workspaceName)
	if err == nil {
		return clusterWorkspace, nil
	}
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	}

	if errors.IsNotFound(err) {
		// give the cache time to observe a recent workspace creation
		time.Sleep(50 * time.Millisecond)
		clusterWorkspace, err = c.lister.Cluster(clusterName).Get(workspaceName)
		if err != nil && !errors.IsNotFound(err) {
			return nil, err
		}
		if err == nil {
			klog.V(4).Infof("found %s in cache after waiting", workspaceName)
			return clusterWorkspace, nil
		}
	}

	// Our watch maybe latent, so we make a best effort to get the object, and only fail if not found
	clusterWorkspace, err = c.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Get(context.TODO(), workspaceName, metav1.GetOptions{})
	// the workspace does not exist, so prevent create and update in that workspace
	if errors.IsNotFound(err) {
		return nil, fmt.Errorf("workspace %s does not exist", workspaceName)
	}
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("found %s via storage lookup", workspaceName)
	return clusterWorkspace, nil
}
