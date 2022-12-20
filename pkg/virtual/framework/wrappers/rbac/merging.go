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

package rbac

import (
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
)

var _ rbaclisters.ClusterRoleBindingLister = (*mergedClusterRoleBindingLister)(nil)

func NewMergedClusterRoleBindingLister(listers ...rbaclisters.ClusterRoleBindingLister) rbaclisters.ClusterRoleBindingLister {
	return &mergedClusterRoleBindingLister{listers: listers}
}

type mergedClusterRoleBindingLister struct {
	listers []rbaclisters.ClusterRoleBindingLister
}

func (l mergedClusterRoleBindingLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRoleBinding, err error) {
	result := make([]*rbacv1.ClusterRoleBinding, 0)
	for _, lister := range l.listers {
		list, err := lister.List(selector)
		if err != nil {
			return nil, err
		}

		for i := range list {
			entry := list[i].DeepCopy()
			entry.Name = logicalcluster.From(entry).String() + ":" + entry.GetName()
			result = append(result, entry)
		}
	}
	return result, nil
}

func (l mergedClusterRoleBindingLister) Get(name string) (*rbacv1.ClusterRoleBinding, error) {
	panic("not implemented")
}

var _ rbaclisters.ClusterRoleLister = (*mergedClusterRoleLister)(nil)

func NewMergedClusterRoleLister(listers ...rbaclisters.ClusterRoleLister) rbaclisters.ClusterRoleLister {
	return &mergedClusterRoleLister{listers: listers}
}

type mergedClusterRoleLister struct {
	listers []rbaclisters.ClusterRoleLister
}

func (l *mergedClusterRoleLister) List(selector labels.Selector) (ret []*rbacv1.ClusterRole, err error) {
	aggregatedList := make([]*rbacv1.ClusterRole, 0)
	for _, lister := range l.listers {
		list, err := lister.List(selector)
		if err != nil {
			return nil, err
		}
		aggregatedList = mergeClusterRoles(aggregatedList, list)
	}
	return aggregatedList, nil
}

func (l *mergedClusterRoleLister) Get(name string) (*rbacv1.ClusterRole, error) {
	var errorHolder error
	var mergedItem *rbacv1.ClusterRole
	for _, lister := range l.listers {
		item, err := lister.Get(name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				errorHolder = err
			}
		} else {
			if mergedItem == nil {
				mergedItem = item
			} else {
				mergedItem = mergeClusterRoles([]*rbacv1.ClusterRole{mergedItem}, []*rbacv1.ClusterRole{item})[0]
			}
		}
	}
	if mergedItem != nil {
		errorHolder = nil
	}
	return mergedItem, errorHolder
}

var _ rbaclisters.RoleLister = (*mergedRoleLister)(nil)
var _ rbaclisters.RoleNamespaceLister = (*mergedRoleNamespaceLister)(nil)

func NewMergedRoleLister(listers ...rbaclisters.RoleLister) rbaclisters.RoleLister {
	return &mergedRoleLister{listers: listers}
}

type mergedRoleLister struct {
	listers []rbaclisters.RoleLister
}

type mergedRoleNamespaceLister struct {
	listers []rbaclisters.RoleNamespaceLister
}

func (l *mergedRoleLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
	aggregatedList := make([]*rbacv1.Role, 0)
	for _, lister := range l.listers {
		list, err := lister.List(selector)
		if err != nil {
			return nil, err
		}
		aggregatedList = append(aggregatedList, list...)
	}
	return aggregatedList, nil
}

func (l *mergedRoleLister) Roles(namespace string) rbaclisters.RoleNamespaceLister {
	aggregatedListers := make([]rbaclisters.RoleNamespaceLister, 0)
	for _, inf := range l.listers {
		aggregatedListers = append(aggregatedListers, inf.Roles(namespace))
	}
	return &mergedRoleNamespaceLister{
		listers: aggregatedListers,
	}
}

func (l *mergedRoleNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.Role, err error) {
	aggregatedList := make([]*rbacv1.Role, 0)
	for _, lister := range l.listers {
		list, err := lister.List(selector)
		if err != nil {
			return nil, err
		}
		aggregatedList = mergeRoles(aggregatedList, list)
	}
	return aggregatedList, nil
}

// TODO: slaskawi - to be converted to generics once we rebase on Go 1.18
func mergeRoles(array []*rbacv1.Role, rolesToBeMergedIn []*rbacv1.Role) []*rbacv1.Role {
	for _, roleToBeMerged := range rolesToBeMergedIn {
		foundIndex := -1
		for i, role := range array {
			if roleToBeMerged.Name == role.Name {
				foundIndex = i
				break
			}
		}
		if foundIndex != -1 {
			array[foundIndex].Rules = append(array[foundIndex].Rules, roleToBeMerged.Rules...)
		} else {
			array = append(array, roleToBeMerged)
		}
	}
	return array
}

// TODO: slaskawi - to be converted to generics once we rebase on Go 1.18
func mergeClusterRoles(array []*rbacv1.ClusterRole, rolesToBeMergedIn []*rbacv1.ClusterRole) []*rbacv1.ClusterRole {
	for _, roleToBeMerged := range rolesToBeMergedIn {
		foundIndex := -1
		for i, role := range array {
			if roleToBeMerged.Name == role.Name {
				foundIndex = i
				break
			}
		}
		if foundIndex != -1 {
			array[foundIndex].Rules = append(array[foundIndex].Rules, roleToBeMerged.Rules...)
		} else {
			array = append(array, roleToBeMerged)
		}
	}
	return array
}

func (l *mergedRoleNamespaceLister) Get(name string) (*rbacv1.Role, error) {
	var errorHolder error
	var mergedItem *rbacv1.Role
	for _, lister := range l.listers {
		item, err := lister.Get(name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				errorHolder = err
			}
		} else {
			if mergedItem == nil {
				mergedItem = item
			} else {
				mergedItem = mergeRoles([]*rbacv1.Role{mergedItem}, []*rbacv1.Role{item})[0]
			}
		}
	}
	if mergedItem != nil {
		errorHolder = nil
	}
	return mergedItem, errorHolder
}
