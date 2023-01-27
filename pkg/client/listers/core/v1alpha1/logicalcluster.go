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

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
)

// LogicalClusterClusterLister can list LogicalClusters across all workspaces, or scope down to a LogicalClusterLister for one workspace.
// All objects returned here must be treated as read-only.
type LogicalClusterClusterLister interface {
	// List lists all LogicalClusters in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error)
	// Cluster returns a lister that can list and get LogicalClusters in one workspace.
	Cluster(clusterName logicalcluster.Name) LogicalClusterLister
	LogicalClusterClusterListerExpansion
}

type logicalClusterClusterLister struct {
	indexer cache.Indexer
}

// NewLogicalClusterClusterLister returns a new LogicalClusterClusterLister.
// We assume that the indexer:
// - is fed by a cross-workspace LIST+WATCH
// - uses kcpcache.MetaClusterNamespaceKeyFunc as the key function
// - has the kcpcache.ClusterIndex as an index
func NewLogicalClusterClusterLister(indexer cache.Indexer) *logicalClusterClusterLister {
	return &logicalClusterClusterLister{indexer: indexer}
}

// List lists all LogicalClusters in the indexer across all workspaces.
func (s *logicalClusterClusterLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*corev1alpha1.LogicalCluster))
	})
	return ret, err
}

// Cluster scopes the lister to one workspace, allowing users to list and get LogicalClusters.
func (s *logicalClusterClusterLister) Cluster(clusterName logicalcluster.Name) LogicalClusterLister {
	return &logicalClusterLister{indexer: s.indexer, clusterName: clusterName}
}

// LogicalClusterLister can list all LogicalClusters, or get one in particular.
// All objects returned here must be treated as read-only.
type LogicalClusterLister interface {
	// List lists all LogicalClusters in the workspace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error)
	// Get retrieves the LogicalCluster from the indexer for a given workspace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*corev1alpha1.LogicalCluster, error)
	LogicalClusterListerExpansion
}

// logicalClusterLister can list all LogicalClusters inside a workspace.
type logicalClusterLister struct {
	indexer     cache.Indexer
	clusterName logicalcluster.Name
}

// List lists all LogicalClusters in the indexer for a workspace.
func (s *logicalClusterLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	err = kcpcache.ListAllByCluster(s.indexer, s.clusterName, selector, func(i interface{}) {
		ret = append(ret, i.(*corev1alpha1.LogicalCluster))
	})
	return ret, err
}

// Get retrieves the LogicalCluster from the indexer for a given workspace and name.
func (s *logicalClusterLister) Get(name string) (*corev1alpha1.LogicalCluster, error) {
	key := kcpcache.ToClusterAwareKey(s.clusterName.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(corev1alpha1.Resource("logicalclusters"), name)
	}
	return obj.(*corev1alpha1.LogicalCluster), nil
}

// NewLogicalClusterLister returns a new LogicalClusterLister.
// We assume that the indexer:
// - is fed by a workspace-scoped LIST+WATCH
// - uses cache.MetaNamespaceKeyFunc as the key function
func NewLogicalClusterLister(indexer cache.Indexer) *logicalClusterScopedLister {
	return &logicalClusterScopedLister{indexer: indexer}
}

// logicalClusterScopedLister can list all LogicalClusters inside a workspace.
type logicalClusterScopedLister struct {
	indexer cache.Indexer
}

// List lists all LogicalClusters in the indexer for a workspace.
func (s *logicalClusterScopedLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	err = cache.ListAll(s.indexer, selector, func(i interface{}) {
		ret = append(ret, i.(*corev1alpha1.LogicalCluster))
	})
	return ret, err
}

// Get retrieves the LogicalCluster from the indexer for a given workspace and name.
func (s *logicalClusterScopedLister) Get(name string) (*corev1alpha1.LogicalCluster, error) {
	key := name
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(corev1alpha1.Resource("logicalclusters"), name)
	}
	return obj.(*corev1alpha1.LogicalCluster), nil
}
