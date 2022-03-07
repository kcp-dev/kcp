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
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	tenancyapis "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

func FilterInformers(clusterName string, informers tenancyinformers.Interface) tenancyinformers.Interface {
	return &filteredInterface{
		clusterName: clusterName,
		informers:   informers,
	}
}

var _ tenancyinformers.Interface = (*filteredInterface)(nil)

type filteredInterface struct {
	clusterName string
	informers   tenancyinformers.Interface
}

func (i *filteredInterface) ClusterWorkspaceTypes() tenancyinformers.ClusterWorkspaceTypeInformer {
	return FilterClusterWorkspaceTypeInformer(i.clusterName, i.informers.ClusterWorkspaceTypes())
}

func (i *filteredInterface) ClusterWorkspaces() tenancyinformers.ClusterWorkspaceInformer {
	return FilterClusterWorkspaceInformer(i.clusterName, i.informers.ClusterWorkspaces())
}

func (i *filteredInterface) WorkspaceShards() tenancyinformers.WorkspaceShardInformer {
	return FilterWorkspaceShardInformer(i.clusterName, i.informers.WorkspaceShards())
}

func FilterClusterWorkspaceTypeInformer(clusterName string, informer tenancyinformers.ClusterWorkspaceTypeInformer) tenancyinformers.ClusterWorkspaceTypeInformer {
	return &filteredClusterWorkspaceTypeInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.ClusterWorkspaceTypeInformer = (*filteredClusterWorkspaceTypeInformer)(nil)
var _ tenancylisters.ClusterWorkspaceTypeLister = (*filteredClusterWorkspaceTypeLister)(nil)

type filteredClusterWorkspaceTypeInformer struct {
	clusterName string
	informer    tenancyinformers.ClusterWorkspaceTypeInformer
}

type filteredClusterWorkspaceTypeLister struct {
	clusterName string
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

func (l *filteredClusterWorkspaceTypeLister) List(selector labels.Selector) (ret []*tenancyapis.ClusterWorkspaceType, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceTypeLister) Get(name string) (*tenancyapis.ClusterWorkspaceType, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *filteredClusterWorkspaceTypeLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyapis.ClusterWorkspaceType, err error) {
	items, err := l.lister.ListWithContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceTypeLister) GetWithContext(ctx context.Context, name string) (*tenancyapis.ClusterWorkspaceType, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

func FilterClusterWorkspaceInformer(clusterName string, informer tenancyinformers.ClusterWorkspaceInformer) tenancyinformers.ClusterWorkspaceInformer {
	return &filteredClusterWorkspaceInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.ClusterWorkspaceInformer = (*filteredClusterWorkspaceInformer)(nil)
var _ tenancylisters.ClusterWorkspaceLister = (*filteredClusterWorkspaceLister)(nil)

type filteredClusterWorkspaceInformer struct {
	clusterName string
	informer    tenancyinformers.ClusterWorkspaceInformer
}

type filteredClusterWorkspaceLister struct {
	clusterName string
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

func (l *filteredClusterWorkspaceLister) List(selector labels.Selector) (ret []*tenancyapis.ClusterWorkspace, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceLister) Get(name string) (*tenancyapis.ClusterWorkspace, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *filteredClusterWorkspaceLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyapis.ClusterWorkspace, err error) {
	items, err := l.lister.ListWithContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredClusterWorkspaceLister) GetWithContext(ctx context.Context, name string) (*tenancyapis.ClusterWorkspace, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

func FilterWorkspaceShardInformer(clusterName string, informer tenancyinformers.WorkspaceShardInformer) tenancyinformers.WorkspaceShardInformer {
	return &filteredWorkspaceShardInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ tenancyinformers.WorkspaceShardInformer = (*filteredWorkspaceShardInformer)(nil)
var _ tenancylisters.WorkspaceShardLister = (*filteredWorkspaceShardLister)(nil)

type filteredWorkspaceShardInformer struct {
	clusterName string
	informer    tenancyinformers.WorkspaceShardInformer
}

type filteredWorkspaceShardLister struct {
	clusterName string
	lister      tenancylisters.WorkspaceShardLister
}

func (i *filteredWorkspaceShardInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredWorkspaceShardInformer) Lister() tenancylisters.WorkspaceShardLister {
	return &filteredWorkspaceShardLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredWorkspaceShardLister) List(selector labels.Selector) (ret []*tenancyapis.WorkspaceShard, err error) {
	items, err := l.lister.List(selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredWorkspaceShardLister) Get(name string) (*tenancyapis.WorkspaceShard, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *filteredWorkspaceShardLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyapis.WorkspaceShard, err error) {
	items, err := l.lister.ListWithContext(ctx, selector)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ClusterName == l.clusterName {
			ret = append(ret, item)
		}
	}
	return
}

func (l *filteredWorkspaceShardLister) GetWithContext(ctx context.Context, name string) (*tenancyapis.WorkspaceShard, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}
