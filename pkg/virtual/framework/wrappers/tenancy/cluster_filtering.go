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

package tenancy

import (
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

func FilterInformers(clusterName logicalcluster.Name, informers tenancyinformers.Interface) tenancyinformers.Interface {
	return &filteredInterface{
		clusterName: clusterName,
		informers:   informers,
	}
}

var _ tenancyinformers.Interface = (*filteredInterface)(nil)

type filteredInterface struct {
	clusterName logicalcluster.Name
	informers   tenancyinformers.Interface
}

func (i *filteredInterface) ClusterWorkspaceTypes() tenancyinformers.ClusterWorkspaceTypeInformer {
	return FilterClusterWorkspaceTypeInformer(i.clusterName, i.informers.ClusterWorkspaceTypes())
}

func (i *filteredInterface) ClusterWorkspaces() tenancyinformers.ClusterWorkspaceInformer {
	return FilterClusterWorkspaceInformer(i.clusterName, i.informers.ClusterWorkspaces())
}

func (i *filteredInterface) ClusterWorkspaceShards() tenancyinformers.ClusterWorkspaceShardInformer {
	return FilterWorkspaceShardInformer(i.clusterName, i.informers.ClusterWorkspaceShards())
}

func FilterClusterWorkspaceTypeInformer(clusterName logicalcluster.Name, informer tenancyinformers.ClusterWorkspaceTypeInformer) tenancyinformers.ClusterWorkspaceTypeInformer {
	return &filteredClusterWorkspaceTypeInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.ClusterWorkspaceTypeInformer = (*filteredClusterWorkspaceTypeInformer)(nil)
var _ tenancylisters.ClusterWorkspaceTypeLister = (*filteredClusterWorkspaceTypeLister)(nil)

type filteredClusterWorkspaceTypeInformer struct {
	clusterName logicalcluster.Name
	informer    tenancyinformers.ClusterWorkspaceTypeInformer
}

type filteredClusterWorkspaceTypeLister struct {
	clusterName logicalcluster.Name
	lister      tenancylisters.ClusterWorkspaceTypeLister
}

func (i *filteredClusterWorkspaceTypeInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredClusterWorkspaceTypeInformer) Lister() tenancylisters.ClusterWorkspaceTypeLister {
	return &filteredClusterWorkspaceTypeLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredClusterWorkspaceTypeLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceType, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if logicalcluster.From(item) == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceTypeLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName.Empty() {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func FilterClusterWorkspaceInformer(clusterName logicalcluster.Name, informer tenancyinformers.ClusterWorkspaceInformer) tenancyinformers.ClusterWorkspaceInformer {
	return &filteredClusterWorkspaceInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.ClusterWorkspaceInformer = (*filteredClusterWorkspaceInformer)(nil)
var _ tenancylisters.ClusterWorkspaceLister = (*filteredClusterWorkspaceLister)(nil)

type filteredClusterWorkspaceInformer struct {
	clusterName logicalcluster.Name
	informer    tenancyinformers.ClusterWorkspaceInformer
}

type filteredClusterWorkspaceLister struct {
	clusterName logicalcluster.Name
	lister      tenancylisters.ClusterWorkspaceLister
}

func (i *filteredClusterWorkspaceInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredClusterWorkspaceInformer) Lister() tenancylisters.ClusterWorkspaceLister {
	return &filteredClusterWorkspaceLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredClusterWorkspaceLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspace, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if logicalcluster.From(item) == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName.Empty() {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func FilterWorkspaceShardInformer(clusterName logicalcluster.Name, informer tenancyinformers.ClusterWorkspaceShardInformer) tenancyinformers.ClusterWorkspaceShardInformer {
	return &filteredWorkspaceShardInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.ClusterWorkspaceShardInformer = (*filteredWorkspaceShardInformer)(nil)
var _ tenancylisters.ClusterWorkspaceShardLister = (*filteredWorkspaceShardLister)(nil)

type filteredWorkspaceShardInformer struct {
	clusterName logicalcluster.Name
	informer    tenancyinformers.ClusterWorkspaceShardInformer
}

type filteredWorkspaceShardLister struct {
	clusterName logicalcluster.Name
	lister      tenancylisters.ClusterWorkspaceShardLister
}

func (i *filteredWorkspaceShardInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredWorkspaceShardInformer) Lister() tenancylisters.ClusterWorkspaceShardLister {
	return &filteredWorkspaceShardLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredWorkspaceShardLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceShard, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if logicalcluster.From(item) == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredWorkspaceShardLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName.Empty() {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}
