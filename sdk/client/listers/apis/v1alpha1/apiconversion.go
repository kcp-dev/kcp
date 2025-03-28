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
	
	"k8s.io/client-go/tools/cache"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/api/errors"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	)

// APIConversionClusterLister can list APIConversions across all workspaces, or scope down to a APIConversionLister for one workspace.
// All objects returned here must be treated as read-only.
type APIConversionClusterLister interface {
	// List lists all APIConversions in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*apisv1alpha1.APIConversion, err error)
	// Cluster returns a lister that can list and get APIConversions in one workspace.
Cluster(clusterName logicalcluster.Name) APIConversionLister
APIConversionClusterListerExpansion
}

type aPIConversionClusterLister struct {
	indexer cache.Indexer
}

// NewAPIConversionClusterLister returns a new APIConversionClusterLister.
// We assume that the indexer:
// - is fed by a cross-workspace LIST+WATCH
// - uses kcpcache.MetaClusterNamespaceKeyFunc as the key function
// - has the kcpcache.ClusterIndex as an index
func NewAPIConversionClusterLister(indexer cache.Indexer) *aPIConversionClusterLister {
	return &aPIConversionClusterLister{indexer: indexer}
}

// List lists all APIConversions in the indexer across all workspaces.
func (s *aPIConversionClusterLister) List(selector labels.Selector) (ret []*apisv1alpha1.APIConversion, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*apisv1alpha1.APIConversion))
	})
	return ret, err
}

// Cluster scopes the lister to one workspace, allowing users to list and get APIConversions.
func (s *aPIConversionClusterLister) Cluster(clusterName logicalcluster.Name) APIConversionLister {
return &aPIConversionLister{indexer: s.indexer, clusterName: clusterName}
}

// APIConversionLister can list all APIConversions, or get one in particular.
// All objects returned here must be treated as read-only.
type APIConversionLister interface {
	// List lists all APIConversions in the workspace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*apisv1alpha1.APIConversion, err error)
// Get retrieves the APIConversion from the indexer for a given workspace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*apisv1alpha1.APIConversion, error)
APIConversionListerExpansion
}
// aPIConversionLister can list all APIConversions inside a workspace.
type aPIConversionLister struct {
	indexer cache.Indexer
	clusterName logicalcluster.Name
}

// List lists all APIConversions in the indexer for a workspace.
func (s *aPIConversionLister) List(selector labels.Selector) (ret []*apisv1alpha1.APIConversion, err error) {
	err = kcpcache.ListAllByCluster(s.indexer, s.clusterName, selector, func(i interface{}) {
		ret = append(ret, i.(*apisv1alpha1.APIConversion))
	})
	return ret, err
}

// Get retrieves the APIConversion from the indexer for a given workspace and name.
func (s *aPIConversionLister) Get(name string) (*apisv1alpha1.APIConversion, error) {
	key := kcpcache.ToClusterAwareKey(s.clusterName.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(apisv1alpha1.Resource("apiconversions"), name)
	}
	return obj.(*apisv1alpha1.APIConversion), nil
}
// NewAPIConversionLister returns a new APIConversionLister.
// We assume that the indexer:
// - is fed by a workspace-scoped LIST+WATCH
// - uses cache.MetaNamespaceKeyFunc as the key function
func NewAPIConversionLister(indexer cache.Indexer) *aPIConversionScopedLister {
	return &aPIConversionScopedLister{indexer: indexer}
}

// aPIConversionScopedLister can list all APIConversions inside a workspace.
type aPIConversionScopedLister struct {
	indexer cache.Indexer
}

// List lists all APIConversions in the indexer for a workspace.
func (s *aPIConversionScopedLister) List(selector labels.Selector) (ret []*apisv1alpha1.APIConversion, err error) {
	err = cache.ListAll(s.indexer, selector, func(i interface{}) {
		ret = append(ret, i.(*apisv1alpha1.APIConversion))
	})
	return ret, err
}

// Get retrieves the APIConversion from the indexer for a given workspace and name.
func (s *aPIConversionScopedLister) Get(name string) (*apisv1alpha1.APIConversion, error) {
	key := name
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(apisv1alpha1.Resource("apiconversions"), name)
	}
	return obj.(*apisv1alpha1.APIConversion), nil
}
