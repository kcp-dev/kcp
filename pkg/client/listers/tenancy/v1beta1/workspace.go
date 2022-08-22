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

package v1beta1

import (
	apimachinerycache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

// WorkspaceClusterLister can list everything or scope by workspace
type WorkspaceClusterLister struct {
	indexer cache.Indexer
}

// NewWorkspaceClusterLister returns a new WorkspaceClusterLister.
func NewWorkspaceClusterLister(indexer cache.Indexer) *WorkspaceClusterLister {
	return &WorkspaceClusterLister{indexer: indexer}
}

// List lists all tenancyv1beta1.Workspace in the indexer.
func (s WorkspaceClusterLister) List(selector labels.Selector) (ret []*tenancyv1beta1.Workspace, err error) {
	err = cache.ListAll(s.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*tenancyv1beta1.Workspace))
	})
	return ret, err
}

// Cluster returns an object that can list and get tenancyv1beta1.Workspace.

func (s WorkspaceClusterLister) Cluster(cluster logicalcluster.Name) *WorkspaceLister {
	return &WorkspaceLister{indexer: s.indexer, cluster: cluster}
}

// WorkspaceLister can list everything inside a cluster or scope by namespace
type WorkspaceLister struct {
	indexer cache.Indexer
	cluster logicalcluster.Name
}

// List lists all tenancyv1beta1.Workspace in the indexer.
func (s WorkspaceLister) List(selector labels.Selector) (ret []*tenancyv1beta1.Workspace, err error) {
	selectAll := selector == nil || selector.Empty()

	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", "")
	list, err := s.indexer.ByIndex(apimachinerycache.ClusterIndexName, key)
	if err != nil {
		return nil, err
	}

	for i := range list {
		obj := list[i].(*tenancyv1beta1.Workspace)
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

// Get retrieves the tenancyv1beta1.Workspace from the indexer for a given name.
func (s WorkspaceLister) Get(name string) (*tenancyv1beta1.Workspace, error) {
	key := apimachinerycache.ToClusterAwareKey(s.cluster.String(), "", name)
	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(tenancyv1beta1.Resource("Workspace"), name)
	}
	return obj.(*tenancyv1beta1.Workspace), nil
}
