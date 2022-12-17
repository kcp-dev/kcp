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
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/validation"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func InitializerPresent(initializer corev1alpha1.LogicalClusterInitializer, initializers []corev1alpha1.LogicalClusterInitializer) bool {
	for i := range initializers {
		if initializers[i] == initializer {
			return true
		}
	}
	return false
}

func EnsureInitializerPresent(initializer corev1alpha1.LogicalClusterInitializer, initializers []corev1alpha1.LogicalClusterInitializer) []corev1alpha1.LogicalClusterInitializer {
	for i := range initializers {
		if initializers[i] == initializer {
			return initializers // already present
		}
	}
	initializers = append(initializers, initializer)
	return initializers
}

func EnsureInitializerAbsent(initializer corev1alpha1.LogicalClusterInitializer, initializers []corev1alpha1.LogicalClusterInitializer) []corev1alpha1.LogicalClusterInitializer {
	removeAt := -1
	for i := range initializers {
		if initializers[i] == initializer {
			removeAt = i
			break
		}
	}
	if removeAt != -1 {
		initializers = append(initializers[:removeAt], initializers[removeAt+1:]...)
	}
	return initializers
}

// InitializerForType determines the identifier for the implicit initializer associated with the WorkspaceType.
func InitializerForType(cwt *tenancyv1alpha1.WorkspaceType) corev1alpha1.LogicalClusterInitializer {
	return corev1alpha1.LogicalClusterInitializer(logicalcluster.From(cwt).Path().Join(cwt.Name).String())
}

// InitializerForReference determines the identifier for the implicit initializer associated with the
// WorkspaceType referred to with the reference.
func InitializerForReference(cwtr tenancyv1alpha1.WorkspaceTypeReference) corev1alpha1.LogicalClusterInitializer {
	return corev1alpha1.LogicalClusterInitializer(cwtr.Path + ":" + string(cwtr.Name))
}

// TypeFrom determines the WorkspaceType workspace and name from an initializer name.
func TypeFrom(initializer corev1alpha1.LogicalClusterInitializer) (logicalcluster.Name, string, error) {
	separatorIndex := strings.LastIndex(string(initializer), ":")
	switch separatorIndex {
	case -1:
		return "", "", fmt.Errorf("expected cluster workspace initializer in form workspace:name, not %q", initializer)
	default:
		return logicalcluster.Name(initializer[:separatorIndex]), tenancyv1alpha1.ObjectName(tenancyv1alpha1.WorkspaceTypeName(initializer[separatorIndex+1:])), nil
	}
}

// InitializerToLabel transforms an initializer into a key-value pair to add to a label set. We use a hash
// to create a unique identifier from this information, prefixing the hash in order to create a value which
// is unlikely to collide, and adding the full hash as a value in order to make it difficult to forge the pair.
func InitializerToLabel(initializer corev1alpha1.LogicalClusterInitializer) (string, string) {
	hash := fmt.Sprintf("%x", sha256.Sum224([]byte(initializer)))
	labelKeyHashLength := validation.LabelValueMaxLength - len(tenancyv1alpha1.WorkspaceInitializerLabelPrefix)
	return tenancyv1alpha1.WorkspaceInitializerLabelPrefix + hash[0:labelKeyHashLength], hash
}
