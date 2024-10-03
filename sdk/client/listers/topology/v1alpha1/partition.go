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
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

// PartitionClusterLister can list Partitions across all workspaces, or scope down to a PartitionLister for one workspace.
// All objects returned here must be treated as read-only.
type PartitionClusterLister interface {
	// List lists all Partitions in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*topologyv1alpha1.Partition, err error)
	// Cluster returns a lister that can list and get Partitions in one workspace.
	Cluster(clusterName logicalcluster.Name) PartitionLister
	PartitionClusterListerExpansion
}

type partitionClusterLister struct {
	indexer cache.Indexer
}

// NewPartitionClusterLister returns a new PartitionClusterLister.
// We assume that the indexer:
// - is fed by a cross-workspace LIST+WATCH
// - uses kcpcache.MetaClusterNamespaceKeyFunc as the key function
// - has the kcpcache.ClusterIndex as an index
func NewPartitionClusterLister(indexer cache.Indexer) *partitionClusterLister {
	return &partitionClusterLister{indexer: indexer}
}

// List lists all Partitions in the indexer across all workspaces.
func (s *partitionClusterLister) List(selector labels.Selector) (ret []*topologyv1alpha1.Partition, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*topologyv1alpha1.Partition))
	})
	return ret, err
}

// Cluster scopes the lister to one workspace, allowing users to list and get Partitions.
func (s *partitionClusterLister) Cluster(clusterName logicalcluster.Name) PartitionLister {
	return &partitionLister{indexer: s.indexer, clusterName: clusterName}
}

// PartitionLister can list all Partitions, or get one in particular.
// All objects returned here must be treated as read-only.
type PartitionLister interface {
	// List lists all Partitions in the workspace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*topologyv1alpha1.Partition, err error)
	// Get retrieves the Partition from the indexer for a given workspace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*topologyv1alpha1.Partition, error)
	PartitionListerExpansion
}

// partitionLister can list all Partitions inside a workspace.
type partitionLister struct {
	indexer     cache.Indexer
	clusterName logicalcluster.Name
}

// List lists all Partitions in the indexer for a workspace.
func (s *partitionLister) List(selector labels.Selector) (ret []*topologyv1alpha1.Partition, err error) {
	err = kcpcache.ListAllByCluster(s.indexer, s.clusterName, selector, func(i interface{}) {
		ret = append(ret, i.(*topologyv1alpha1.Partition))
	})
	return ret, err
}

// Get retrieves the Partition from the indexer for a given workspace and name.
func (s *partitionLister) Get(name string) (*topologyv1alpha1.Partition, error) {
	key := kcpcache.ToClusterAwareKey(s.clusterName.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(topologyv1alpha1.Resource("partitions"), name)
	}
	return obj.(*topologyv1alpha1.Partition), nil
}

// NewPartitionLister returns a new PartitionLister.
// We assume that the indexer:
// - is fed by a workspace-scoped LIST+WATCH
// - uses cache.MetaNamespaceKeyFunc as the key function
func NewPartitionLister(indexer cache.Indexer) *partitionScopedLister {
	return &partitionScopedLister{indexer: indexer}
}

// partitionScopedLister can list all Partitions inside a workspace.
type partitionScopedLister struct {
	indexer cache.Indexer
}

// List lists all Partitions in the indexer for a workspace.
func (s *partitionScopedLister) List(selector labels.Selector) (ret []*topologyv1alpha1.Partition, err error) {
	err = cache.ListAll(s.indexer, selector, func(i interface{}) {
		ret = append(ret, i.(*topologyv1alpha1.Partition))
	})
	return ret, err
}

// Get retrieves the Partition from the indexer for a given workspace and name.
func (s *partitionScopedLister) Get(name string) (*topologyv1alpha1.Partition, error) {
	key := name
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(topologyv1alpha1.Resource("partitions"), name)
	}
	return obj.(*topologyv1alpha1.Partition), nil
}