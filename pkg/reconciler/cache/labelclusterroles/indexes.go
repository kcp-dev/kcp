/*
Copyright 2023 The KCP Authors.

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

package labelclusterroles

import (
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/kubernetes/pkg/apis/rbac"
)

const ClusterRoleBindingByClusterRoleName = "indexClusterRoleBindingByClusterRole"

func IndexClusterRoleBindingByClusterRoleName(obj interface{}) ([]string, error) {
	crb, ok := obj.(*rbacv1.ClusterRoleBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExportEndpointSlice", obj)
	}

	if crb.RoleRef.APIGroup == rbac.GroupName && crb.RoleRef.Kind == "ClusterRole" {
		key := kcpcache.ToClusterAwareKey(logicalcluster.From(crb).String(), "", crb.RoleRef.Name)
		return []string{key}, nil
	}

	return []string{}, nil
}
