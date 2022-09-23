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

package helper

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func IsValidCluster(cluster logicalcluster.Name) bool {
	if !cluster.IsValid() {
		return false
	}

	return cluster.HasPrefix(v1alpha1.RootCluster) || cluster.HasPrefix(logicalcluster.New("system"))
}

func QualifiedObjectName(obj metav1.Object) string {
	if len(obj.GetNamespace()) > 0 {
		return fmt.Sprintf("%s|%s/%s", logicalcluster.From(obj), obj.GetNamespace(), obj.GetName())
	}
	return fmt.Sprintf("%s|%s", logicalcluster.From(obj), obj.GetName())
}
