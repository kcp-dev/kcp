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

var _ rbacinformers.Interface = (*singleClusterRBACInformers)(nil)

type singleClusterRBACInformers struct {
	clusterName string
	informers   rbacinformers.Interface
}

func (i *singleClusterRBACInformers) ClusterRoleBindings() rbacinformers.ClusterRoleBindingInformer {
	return &singleClusterCRBInformer{
		informer:    i.informers.ClusterRoleBindings(),
		clusterName: i.clusterName,
	}
}

func (i *singleClusterRBACInformers) ClusterRoles() rbacinformers.ClusterRoleInformer {
	return &singleClusterCRInformer{
		informer:    i.informers.ClusterRoles(),
		clusterName: i.clusterName,
	}
}

func (i *singleClusterRBACInformers) RoleBindings() rbacinformers.RoleBindingInformer {
	return &singleClusterRBInformer{
		informer:    i.informers.RoleBindings(),
		clusterName: i.clusterName,
	}
}

func (i *singleClusterRBACInformers) Roles() rbacinformers.RoleInformer {
	return &singleClusterRInformer{
		informer:    i.informers.Roles(),
		clusterName: i.clusterName,
	}
}

var _ rbacinformers.ClusterRoleBindingInformer = (*singleClusterCRBInformer)(nil)
var _ rbaclisters.ClusterRoleBindingLister = (*singleClusterCRBLister)(nil)

type singleClusterCRBInformer struct {
	clusterName string
	informer    rbacinformers.ClusterRoleBindingInformer
}

type singleClusterCRBLister struct {
	clusterName string
	lister      rbaclisters.ClusterRoleBindingLister
}

func (i *singleClusterCRBInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *singleClusterCRBInformer) Lister() rbaclisters.ClusterRoleBindingLister {
	return &singleClusterCRBLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *singleClusterCRBLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRoleBinding, err error) {
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

func (l *singleClusterCRBLister) Get(name string) (*rbacv1.ClusterRoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *singleClusterCRBLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.ClusterRoleBinding, err error) {
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

func (l *singleClusterCRBLister) GetWithContext(ctx context.Context, name string) (*rbacv1.ClusterRoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

var _ rbacinformers.ClusterRoleInformer = (*singleClusterCRInformer)(nil)
var _ rbaclisters.ClusterRoleLister = (*singleClusterCRLister)(nil)

type singleClusterCRInformer struct {
	clusterName string
	informer    rbacinformers.ClusterRoleInformer
}

type singleClusterCRLister struct {
	clusterName string
	lister      rbaclisters.ClusterRoleLister
}

func (i *singleClusterCRInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *singleClusterCRInformer) Lister() rbaclisters.ClusterRoleLister {
	return &singleClusterCRLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *singleClusterCRLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRole, err error) {
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

func (l *singleClusterCRLister) Get(name string) (*rbacv1.ClusterRole, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func (l *singleClusterCRLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.ClusterRole, err error) {
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

func (l *singleClusterCRLister) GetWithContext(ctx context.Context, name string) (*rbacv1.ClusterRole, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.GetWithContext(ctx, name)
}

var _ rbacinformers.RoleBindingInformer = (*singleClusterRBInformer)(nil)
var _ rbaclisters.RoleBindingLister = (*singleClusterRBLister)(nil)
var _ rbaclisters.RoleBindingNamespaceLister = (*singleClusterRBNamespaceLister)(nil)

type singleClusterRBInformer struct {
	clusterName string
	informer    rbacinformers.RoleBindingInformer
}

type singleClusterRBLister struct {
	clusterName string
	lister      rbaclisters.RoleBindingLister
}

type singleClusterRBNamespaceLister struct {
	clusterName string
	lister      rbaclisters.RoleBindingNamespaceLister
}

func (i *singleClusterRBInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *singleClusterRBInformer) Lister() rbaclisters.RoleBindingLister {
	return &singleClusterRBLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *singleClusterRBLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *singleClusterRBLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *singleClusterRBLister) RoleBindings(namespace string) rbaclisters.RoleBindingNamespaceLister {
	return &singleClusterRBNamespaceLister{
		clusterName: l.clusterName,
		lister:      l.lister.RoleBindings(namespace),
	}
}

func (l *singleClusterRBNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
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

func (l *singleClusterRBNamespaceLister) Get(name string) (*rbacv1.RoleBinding, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

var _ rbacinformers.RoleInformer = (*singleClusterRInformer)(nil)
var _ rbaclisters.RoleLister = (*singleClusterRLister)(nil)
var _ rbaclisters.RoleNamespaceLister = (*singleClusterRNamespaceLister)(nil)

type singleClusterRInformer struct {
	clusterName string
	informer    rbacinformers.RoleInformer
}

type singleClusterRLister struct {
	clusterName string
	lister      rbaclisters.RoleLister
}

type singleClusterRNamespaceLister struct {
	clusterName string
	lister      rbaclisters.RoleNamespaceLister
}

func (i *singleClusterRInformer) Informer() cache.SharedIndexInformer {
	return i.informer.Informer()
}

func (i *singleClusterRInformer) Lister() rbaclisters.RoleLister {
	return &singleClusterRLister{
		clusterName: i.clusterName,
		lister:      i.informer.Lister(),
	}
}

func (l *singleClusterRLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *singleClusterRLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *singleClusterRLister) Roles(namespace string) rbaclisters.RoleNamespaceLister {
	return &singleClusterRNamespaceLister{
		clusterName: l.clusterName,
		lister:      l.lister.Roles(namespace),
	}
}

func (l *singleClusterRNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
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

func (l *singleClusterRNamespaceLister) Get(name string) (*rbacv1.Role, error) {
	if clusterName, _ := clusters.SplitClusterAwareKey(name); clusterName == "" {
		name = clusters.ToClusterAwareKey(l.clusterName, name)
	}
	return l.lister.Get(name)
}

func FilterPerCluster(clusterName string, informers rbacinformers.Interface) rbacinformers.Interface {
	return &singleClusterRBACInformers{
		clusterName: clusterName,
		informers:   informers,
	}
}
