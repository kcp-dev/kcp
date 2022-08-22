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

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
)

// APIResourceImportClusterLister can list everything or scope by workspace
type APIResourceImportClusterLister struct {
	indexer cache.Indexer
}

// NewAPIResourceImportClusterLister returns a new APIResourceImportClusterLister.
func NewAPIResourceImportClusterLister(indexer cache.Indexer) *APIResourceImportClusterLister {
	return &APIResourceImportClusterLister{indexer: indexer}
}

// List lists all apiresourcev1alpha1.APIResourceImport in the indexer.
func (s APIResourceImportClusterLister) List(selector labels.Selector) (ret []*apiresourcev1alpha1.APIResourceImport, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*apiresourcev1alpha1.APIResourceImport))
	})
	return ret, err
}

// Cluster returns an object that can list and get apiresourcev1alpha1.APIResourceImport.

func (s APIResourceImportClusterLister) Cluster(cluster logicalcluster.Name) *APIResourceImportLister {
	return &APIResourceImportLister{indexer: s.indexer, cluster: cluster}
}

// APIResourceImportLister can list everything inside a cluster or scope by namespace
type APIResourceImportLister struct {
	indexer cache.Indexer
	cluster logicalcluster.Name
}

// List lists all apiresourcev1alpha1.APIResourceImport in the indexer.
func (s APIResourceImportLister) List(selector labels.Selector) (ret []*apiresourcev1alpha1.APIResourceImport, err error) {
	selectAll := selector == nil || selector.Empty()

	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", "")
	list, err := s.indexer.ByIndex(apimachinerycache.ClusterIndexName, key)
	if err != nil {
		return nil, err
	}

	for i := range list {
		obj := list[i].(*apiresourcev1alpha1.APIResourceImport)
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

// Get retrieves the apiresourcev1alpha1.APIResourceImport from the indexer for a given name.
func (s APIResourceImportLister) Get(name string) (*apiresourcev1alpha1.APIResourceImport, error) {
	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(apiresourcev1alpha1.Resource("APIResourceImport"), name)
	}
	return obj.(*apiresourcev1alpha1.APIResourceImport), nil
}
