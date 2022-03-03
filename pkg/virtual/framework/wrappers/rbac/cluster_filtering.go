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

package rbac

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
)

func FilterInformers(clusterName string, informers rbacinformers.Interface) rbacinformers.Interface {
	return &filteredInterface{
		clusterName: clusterName,
		informers:   informers,
	}
}

var _ rbacinformers.Interface = (*filteredInterface)(nil)

type filteredInterface struct {
	clusterName string
	informers   rbacinformers.Interface
}

func (i *filteredInterface) ClusterRoleBindings() rbacinformers.ClusterRoleBindingInformer {
	return FilterClusterRoleBindingInformer(i.clusterName, i.informers.ClusterRoleBindings())
}

func (i *filteredInterface) ClusterRoles() rbacinformers.ClusterRoleInformer {
	return FilterClusterRoleInformer(i.clusterName, i.informers.ClusterRoles())
}

func (i *filteredInterface) RoleBindings() rbacinformers.RoleBindingInformer {
	return FilterRoleBindingInformer(i.clusterName, i.informers.RoleBindings())
}

func (i *filteredInterface) Roles() rbacinformers.RoleInformer {
	return FilterRoleInformer(i.clusterName, i.informers.Roles())
}

func FilterClusterRoleBindingInformer(clusterName string, informer rbacinformers.ClusterRoleBindingInformer) rbacinformers.ClusterRoleBindingInformer {
	return &filteredClusterRoleBindingInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ rbacinformers.ClusterRoleBindingInformer = (*filteredClusterRoleBindingInformer)(nil)
var _ rbaclisters.ClusterRoleBindingLister = (*filteredClusterRoleBindingLister)(nil)

type filteredClusterRoleBindingInformer struct {
	clusterName string
	informer    rbacinformers.ClusterRoleBindingInformer
}

type filteredClusterRoleBindingLister struct {
	clusterName string
	lister      rbaclisters.ClusterRoleBindingLister
}

func (i *filteredClusterRoleBindingInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredClusterRoleBindingInformer) Lister() rbaclisters.ClusterRoleBindingLister {
	return &filteredClusterRoleBindingLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredClusterRoleBindingLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRoleBinding, err error) {
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

func (l *filteredClusterRoleBindingLister) Get(name string) (*rbacv1.ClusterRoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *filteredClusterRoleBindingLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.ClusterRoleBinding, err error) {
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

func (l *filteredClusterRoleBindingLister) GetWithContext(ctx context.Context, name string) (*rbacv1.ClusterRoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

func FilterClusterRoleInformer(clusterName string, informer rbacinformers.ClusterRoleInformer) rbacinformers.ClusterRoleInformer {
	return &filteredClusterRoleInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ rbacinformers.ClusterRoleInformer = (*filteredClusterRoleInformer)(nil)
var _ rbaclisters.ClusterRoleLister = (*filteredClusterRoleLister)(nil)

type filteredClusterRoleInformer struct {
	clusterName string
	informer    rbacinformers.ClusterRoleInformer
}

type filteredClusterRoleLister struct {
	clusterName string
	lister      rbaclisters.ClusterRoleLister
}

func (i *filteredClusterRoleInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredClusterRoleInformer) Lister() rbaclisters.ClusterRoleLister {
	return &filteredClusterRoleLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredClusterRoleLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRole, err error) {
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

func (l *filteredClusterRoleLister) Get(name string) (*rbacv1.ClusterRole, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *filteredClusterRoleLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.ClusterRole, err error) {
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

func (l *filteredClusterRoleLister) GetWithContext(ctx context.Context, name string) (*rbacv1.ClusterRole, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

func FilterRoleBindingInformer(clusterName string, informer rbacinformers.RoleBindingInformer) rbacinformers.RoleBindingInformer {
	return &filteredRoleBindingInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ rbacinformers.RoleBindingInformer = (*filteredRoleBindingInformer)(nil)
var _ rbaclisters.RoleBindingLister = (*filteredRoleBindingLister)(nil)
var _ rbaclisters.RoleBindingNamespaceLister = (*filteredRoleBindingNamespaceLister)(nil)

type filteredRoleBindingInformer struct {
	clusterName string
	informer    rbacinformers.RoleBindingInformer
}

type filteredRoleBindingLister struct {
	clusterName string
	lister      rbaclisters.RoleBindingLister
}

type filteredRoleBindingNamespaceLister struct {
	clusterName string
	lister      rbaclisters.RoleBindingNamespaceLister
}

func (i *filteredRoleBindingInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredRoleBindingInformer) Lister() rbaclisters.RoleBindingLister {
	return &filteredRoleBindingLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredRoleBindingLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *filteredRoleBindingLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *filteredRoleBindingLister) RoleBindings(namespace string) rbaclisters.RoleBindingNamespaceLister {
	return &filteredRoleBindingNamespaceLister{
		clusterName: l.clusterName,
		lister:      l.lister.RoleBindings(namespace),
	}
}

func (l *filteredRoleBindingNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *filteredRoleBindingNamespaceLister) Get(name string) (*rbacv1.RoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func FilterRoleInformer(clusterName string, informer rbacinformers.RoleInformer) rbacinformers.RoleInformer {
	return &filteredRoleInformer{
		clusterName: clusterName,
		informer:    informer,
	}
}

var _ rbacinformers.RoleInformer = (*filteredRoleInformer)(nil)
var _ rbaclisters.RoleLister = (*filteredRoleLister)(nil)
var _ rbaclisters.RoleNamespaceLister = (*filteredRoleNamespaceLister)(nil)

type filteredRoleInformer struct {
	clusterName string
	informer    rbacinformers.RoleInformer
}

type filteredRoleLister struct {
	clusterName string
	lister      rbaclisters.RoleLister
}

type filteredRoleNamespaceLister struct {
	clusterName string
	lister      rbaclisters.RoleNamespaceLister
}

func (i *filteredRoleInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *filteredRoleInformer) Lister() rbaclisters.RoleLister {
	return &filteredRoleLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *filteredRoleLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *filteredRoleLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *filteredRoleLister) Roles(namespace string) rbaclisters.RoleNamespaceLister {
	return &filteredRoleNamespaceLister{
		clusterName: l.clusterName,
		lister:      l.lister.Roles(namespace),
	}
}

func (l *filteredRoleNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *filteredRoleNamespaceLister) Get(name string) (*rbacv1.Role, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}
