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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"

	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceClient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
)

// NewClusterWorkspaceCache returns a non-initialized ClusterWorkspaceCache. The cache needs to be run to begin functioning
func NewClusterWorkspaceCache(workspaces cache.SharedIndexInformer, client workspaceClient.ClusterWorkspaceInterface, defaultNodeSelector string) *ClusterWorkspaceCache {
	if err := workspaces.GetIndexer().AddIndexers(cache.Indexers{
		"requester": indexWorkspaceByRequester,
	}); err != nil {
		panic(err)
	}
	return &ClusterWorkspaceCache{
		Client:              client,
		Store:               workspaces.GetIndexer(),
		HasSynced:           workspaces.GetController().HasSynced,
		DefaultNodeSelector: defaultNodeSelector,
	}
}

type ClusterWorkspaceCache struct {
	Client              workspaceClient.ClusterWorkspaceInterface
	Store               cache.Indexer
	HasSynced           cache.InformerSynced
	DefaultNodeSelector string
}

func (p *ClusterWorkspaceCache) GetWorkspace(name string) (*workspaceapi.ClusterWorkspace, error) {
	key := &workspaceapi.ClusterWorkspace{ObjectMeta: metav1.ObjectMeta{Name: name}}

	// check for cluster workspace in the cache
	clusterWorkspaceObj, exists, err := p.Store.Get(key)
	if err != nil {
		return nil, err
	}

	if !exists {
		// give the cache time to observe a recent workspace creation
		time.Sleep(50 * time.Millisecond)
		clusterWorkspaceObj, exists, err = p.Store.Get(key)
		if err != nil {
			return nil, err
		}
		if exists {
			klog.V(4).Infof("found %s in cache after waiting", name)
		}
	}

	var clusterWorkspace *workspaceapi.ClusterWorkspace
	if exists {
		clusterWorkspace = clusterWorkspaceObj.(*workspaceapi.ClusterWorkspace)
	} else {
		// Our watch maybe latent, so we make a best effort to get the object, and only fail if not found
		clusterWorkspace, err = p.Client.Get(context.TODO(), name, metav1.GetOptions{})
		// the workspace does not exist, so prevent create and update in that workspace
		if err != nil {
			return nil, fmt.Errorf("workspace %s does not exist", name)
		}
		klog.V(4).Infof("found %s via storage lookup", name)
	}
	return clusterWorkspace, nil
}

// Run waits until the cache has synced.
func (c *ClusterWorkspaceCache) Run(stopCh <-chan struct{}) {
	defer runtime.HandleCrash()
	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		return
	}
	<-stopCh
}

// Running determines if the cache is initialized and running
func (c *ClusterWorkspaceCache) Running() bool {
	return c.Store != nil
}

// NewFake is used for testing purpose only
func NewFake(c workspaceClient.ClusterWorkspaceInterface, store cache.Indexer, defaultNodeSelector string) *ClusterWorkspaceCache {
	return &ClusterWorkspaceCache{
		Client:              c,
		Store:               store,
		DefaultNodeSelector: defaultNodeSelector,
	}
}

// NewCacheStore creates an Indexer store with the given key function
func NewCacheStore(keyFn cache.KeyFunc) cache.Indexer {
	return cache.NewIndexer(keyFn, cache.Indexers{
		"requester": indexWorkspaceByRequester,
	})
}

// indexWorkspaceByRequester returns the requester for a given workspace object as an index value
func indexWorkspaceByRequester(obj interface{}) ([]string, error) {
	requester := obj.(*workspaceapi.ClusterWorkspace).Annotations["kcp.dev/workspace-requester"]
	return []string{requester}, nil
}
