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
	"k8s.io/apimachinery/pkg/api/equality"
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
		aggregatedList = shallowCopyAndMergeClusterRoles(aggregatedList, list)
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
				mergedItem = shallowCopyAndMergeClusterRoles([]*rbacv1.ClusterRole{mergedItem}, []*rbacv1.ClusterRole{item})[0]
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
		aggregatedList = shallowCopyAndMergeRoles(aggregatedList, list)
	}
	return aggregatedList, nil
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
				mergedItem = shallowCopyAndMergeRoles([]*rbacv1.Role{mergedItem}, []*rbacv1.Role{item})[0]
			}
		}
	}
	if mergedItem != nil {
		errorHolder = nil
	}
	return mergedItem, errorHolder
}

var _ rbaclisters.RoleBindingLister = (*mergedRoleBindingLister)(nil)
var _ rbaclisters.RoleBindingNamespaceLister = (*mergedRoleBindingNamespaceLister)(nil)

func NewMergedRoleBindingLister(listers ...rbaclisters.RoleBindingLister) rbaclisters.RoleBindingLister {
	return &mergedRoleBindingLister{listers: listers}
}

type mergedRoleBindingLister struct {
	listers []rbaclisters.RoleBindingLister
}

func (l *mergedRoleBindingLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
	result := make([]*rbacv1.RoleBinding, 0)
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

func (l *mergedRoleBindingLister) Get(name string) (*rbacv1.RoleBinding, error) {
	panic("not implemented")
}

func (l *mergedRoleBindingLister) RoleBindings(namespace string) rbaclisters.RoleBindingNamespaceLister {
	aggregatedListers := make([]rbaclisters.RoleBindingNamespaceLister, 0)
	for _, inf := range l.listers {
		aggregatedListers = append(aggregatedListers, inf.RoleBindings(namespace))
	}
	return &mergedRoleBindingNamespaceLister{
		listers: aggregatedListers,
	}
}

type mergedRoleBindingNamespaceLister struct {
	listers []rbaclisters.RoleBindingNamespaceLister
}

func (l mergedRoleBindingNamespaceLister) List(selector labels.Selector) (ret []*rbacv1.RoleBinding, err error) {
	result := make([]*rbacv1.RoleBinding, 0)
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

func (l mergedRoleBindingNamespaceLister) Get(name string) (*rbacv1.RoleBinding, error) {
	panic("implement me")
}

func shallowCopyAndMergeRoles(current []*rbacv1.Role, cachedRoles []*rbacv1.Role) []*rbacv1.Role {
newroles:
	for _, cachedRole := range cachedRoles {
		// Have we already seen this Role?
		for i, existing := range current {
			if existing.Name == cachedRole.Name {
				newRules := mergeRules(existing.Rules, cachedRole.Rules)
				if len(newRules) != len(existing.Rules) {
					// Rules are different, we need to modify the existing ruleset
					// because we're operating on the cache object, make a shallow copy
					// here and modify the shallow copy's rules.
					shallow := *existing
					shallow.Rules = newRules
					current[i] = &shallow
				}
				continue newroles
			}
		}

		// The Role is a new one, append it to the current array without
		// a copy yet.
		current = append(current, cachedRole)
	}
	return current
}

func shallowCopyAndMergeClusterRoles(current []*rbacv1.ClusterRole, cachedRoles []*rbacv1.ClusterRole) []*rbacv1.ClusterRole {
newroles:
	for _, cachedRole := range cachedRoles {
		// Have we already seen this Role?
		for i, existing := range current {
			if existing.Name == cachedRole.Name {
				newRules := mergeRules(existing.Rules, cachedRole.Rules)
				if len(newRules) != len(existing.Rules) {
					// Rules are different, we need to modify the existing ruleset
					// because we're operating on the cache object, make a shallow copy
					// here and modify the shallow copy's rules.
					shallow := *existing
					shallow.Rules = newRules
					current[i] = &shallow
				}
				continue newroles
			}
		}

		// The Role is a new one, append it to the current array without
		// a copy yet.
		current = append(current, cachedRole)
	}
	return current
}

// mergeRules merges PolicyRules if they're exactly the same.
// TODO(vincepri): Figure out if we can actually do better here;
// Rules can probably be deduped based on a number of heuristics.
func mergeRules(current []rbacv1.PolicyRule, cached []rbacv1.PolicyRule) []rbacv1.PolicyRule {
newrules:
	for _, rule := range cached {
		for _, existing := range current {
			if equality.Semantic.DeepEqual(existing, rule) {
				continue newrules
			}
		}
		// The rule is new, merge it in.
		current = append(current, rule)
	}
	return current
}
