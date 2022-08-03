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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// NewClusterWorkspaceCache returns a wrapper around an informer. It serves from the informer, and on cache-miss
// it looks up through the given client.
func NewClusterWorkspaceCache(workspaces cache.SharedIndexInformer, kcpClusterClient kcpclient.ClusterInterface) *ClusterWorkspaceCache {
	return &ClusterWorkspaceCache{
		kcpClusterClient: kcpClusterClient,
		Store:            workspaces.GetIndexer(),
		HasSynced:        workspaces.GetController().HasSynced,
	}
}

type ClusterWorkspaceCache struct {
	kcpClusterClient kcpclient.ClusterInterface
	Store            cache.Indexer
	HasSynced        cache.InformerSynced
}

func (c *ClusterWorkspaceCache) Get(clusterName logicalcluster.Name, workspaceName string) (*workspaceapi.ClusterWorkspace, error) {
	key := &workspaceapi.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName.String(),
			},
			Name: workspaceName,
		},
	}

	// check for cluster workspace in the cache
	clusterWorkspaceObj, exists, err := c.Store.Get(key)
	if err != nil {
		return nil, err
	}

	if !exists {
		// give the cache time to observe a recent workspace creation
		time.Sleep(50 * time.Millisecond)
		clusterWorkspaceObj, exists, err = c.Store.Get(key)
		if err != nil {
			return nil, err
		}
		if exists {
			klog.V(4).Infof("found %s in cache after waiting", workspaceName)
		}
	}

	var clusterWorkspace *workspaceapi.ClusterWorkspace
	if exists {
		clusterWorkspace = clusterWorkspaceObj.(*workspaceapi.ClusterWorkspace)
	} else {
		// Our watch maybe latent, so we make a best effort to get the object, and only fail if not found
		clusterWorkspace, err = c.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Get(context.TODO(), workspaceName, metav1.GetOptions{})
		// the workspace does not exist, so prevent create and update in that workspace
		if err != nil {
			return nil, fmt.Errorf("workspace %s does not exist", workspaceName)
		}
		klog.V(4).Infof("found %s via storage lookup", workspaceName)
	}
	return clusterWorkspace, nil
}
