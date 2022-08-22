//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright The KCP Authors.

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

// Code generated by kcp code-generator. DO NOT EDIT.

package v1alpha1

import (
	apimachinerycache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// ClusterWorkspaceShardClusterLister can list everything or scope by workspace
type ClusterWorkspaceShardClusterLister struct {
	indexer cache.Indexer
}

// NewClusterWorkspaceShardClusterLister returns a new ClusterWorkspaceShardClusterLister.
func NewClusterWorkspaceShardClusterLister(indexer cache.Indexer) *ClusterWorkspaceShardClusterLister {
	return &ClusterWorkspaceShardClusterLister{indexer: indexer}
}

// List lists all tenancyv1alpha1.ClusterWorkspaceShard in the indexer.
func (s ClusterWorkspaceShardClusterLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceShard, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*tenancyv1alpha1.ClusterWorkspaceShard))
	})
	return ret, err
}

// Cluster returns an object that can list and get tenancyv1alpha1.ClusterWorkspaceShard.

func (s ClusterWorkspaceShardClusterLister) Cluster(cluster logicalcluster.Name) *ClusterWorkspaceShardLister {
	return &ClusterWorkspaceShardLister{indexer: s.indexer, cluster: cluster}
}

// ClusterWorkspaceShardLister can list everything inside a cluster or scope by namespace
type ClusterWorkspaceShardLister struct {
	indexer cache.Indexer
	cluster logicalcluster.Name
}

// List lists all tenancyv1alpha1.ClusterWorkspaceShard in the indexer.
func (s ClusterWorkspaceShardLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceShard, err error) {
	selectAll := selector == nil || selector.Empty()

	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", "")
	list, err := s.indexer.ByIndex(apimachinerycache.ClusterIndexName, key)
	if err != nil {
		return nil, err
	}

	for i := range list {
		obj := list[i].(*tenancyv1alpha1.ClusterWorkspaceShard)
		if selectAll {
			ret = append(ret, obj)
		} else {
			if selector.Matches(labels.Set(obj.GetLabels())) {
				ret = append(ret, obj)
			}
		}
	}

	return ret, err
}

// Get retrieves the tenancyv1alpha1.ClusterWorkspaceShard from the indexer for a given name.
func (s ClusterWorkspaceShardLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(tenancyv1alpha1.Resource("ClusterWorkspaceShard"), name)
	}
	return obj.(*tenancyv1alpha1.ClusterWorkspaceShard), nil
}
