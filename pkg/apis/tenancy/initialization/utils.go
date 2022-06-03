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

package initialization

import (
	"crypto/sha256"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func InitializerPresent(initializer tenancyv1alpha1.ClusterWorkspaceInitializer, initializers []tenancyv1alpha1.ClusterWorkspaceInitializer) bool {
	for i := range initializers {
		if equal(initializers[i], initializer) {
			return true
		}
	}
	return false
}

func EnsureInitializerPresent(initializer tenancyv1alpha1.ClusterWorkspaceInitializer, initializers []tenancyv1alpha1.ClusterWorkspaceInitializer) []tenancyv1alpha1.ClusterWorkspaceInitializer {
	for i := range initializers {
		if equal(initializers[i], initializer) {
			return initializers // already present
		}
	}
	initializers = append(initializers, initializer)
	return initializers
}

func EnsureInitializerAbsent(initializer tenancyv1alpha1.ClusterWorkspaceInitializer, initializers []tenancyv1alpha1.ClusterWorkspaceInitializer) []tenancyv1alpha1.ClusterWorkspaceInitializer {
	removeAt := -1
	for i := range initializers {
		if equal(initializers[i], initializer) {
			removeAt = i
			break
		}
	}
	if removeAt != -1 {
		initializers = append(initializers[:removeAt], initializers[removeAt+1:]...)
	}
	return initializers
}

func equal(a, b tenancyv1alpha1.ClusterWorkspaceInitializer) bool {
	return a.Name == b.Name && a.Path == b.Path
}

// InitializerToLabel transforms an initializer into a key-value pair to add to a label set. We use a hash
// to create a unique identifier from this information, prefixing the hash in order to create a value which
// is unlikely to collide, and adding the full hash as a value in order to make it difficult to forge the pair.
func InitializerToLabel(initializer tenancyv1alpha1.ClusterWorkspaceInitializer) (string, string) {
	hash := fmt.Sprintf("%x", sha256.Sum224([]byte(initializer.Path+initializer.Name)))
	labelKeyHashLength := validation.LabelValueMaxLength - len(tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix)
	return tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix + hash[0:labelKeyHashLength], hash
}
