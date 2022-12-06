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

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func InitializerPresent(initializer tenancyv1alpha1.WorkspaceInitializer, initializers []tenancyv1alpha1.WorkspaceInitializer) bool {
	for i := range initializers {
		if initializers[i] == initializer {
			return true
		}
	}
	return false
}

func EnsureInitializerPresent(initializer tenancyv1alpha1.WorkspaceInitializer, initializers []tenancyv1alpha1.WorkspaceInitializer) []tenancyv1alpha1.WorkspaceInitializer {
	for i := range initializers {
		if initializers[i] == initializer {
			return initializers // already present
		}
	}
	initializers = append(initializers, initializer)
	return initializers
}

func EnsureInitializerAbsent(initializer tenancyv1alpha1.WorkspaceInitializer, initializers []tenancyv1alpha1.WorkspaceInitializer) []tenancyv1alpha1.WorkspaceInitializer {
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

// InitializerForType determines the identifier for the implicit initializer associated with the ClusterWorkspaceType.
func InitializerForType(cwt *tenancyv1alpha1.ClusterWorkspaceType) tenancyv1alpha1.WorkspaceInitializer {
	return tenancyv1alpha1.WorkspaceInitializer(logicalcluster.From(cwt).Join(cwt.Name).String())
}

// InitializerForReference determines the identifier for the implicit initializer associated with the
// ClusterWorkspaceType referred to with the reference.
func InitializerForReference(cwtr tenancyv1alpha1.ClusterWorkspaceTypeReference) tenancyv1alpha1.WorkspaceInitializer {
	return tenancyv1alpha1.WorkspaceInitializer(cwtr.Path + ":" + string(cwtr.Name))
}

// TypeFrom determines the ClusterWorkspaceType workspace and name from an initializer name.
func TypeFrom(initializer tenancyv1alpha1.WorkspaceInitializer) (logicalcluster.Path, string, error) {
	separatorIndex := strings.LastIndex(string(initializer), ":")
	switch separatorIndex {
	case -1:
		return logicalcluster.Path{}, "", fmt.Errorf("expected cluster workspace initializer in form workspace:name, not %q", initializer)
	default:
		return logicalcluster.New(string(initializer[:separatorIndex])), tenancyv1alpha1.ObjectName(tenancyv1alpha1.ClusterWorkspaceTypeName(initializer[separatorIndex+1:])), nil
	}
}

// InitializerToLabel transforms an initializer into a key-value pair to add to a label set. We use a hash
// to create a unique identifier from this information, prefixing the hash in order to create a value which
// is unlikely to collide, and adding the full hash as a value in order to make it difficult to forge the pair.
func InitializerToLabel(initializer tenancyv1alpha1.WorkspaceInitializer) (string, string) {
	hash := fmt.Sprintf("%x", sha256.Sum224([]byte(initializer)))
	labelKeyHashLength := validation.LabelValueMaxLength - len(tenancyv1alpha1.WorkspaceInitializerLabelPrefix)
	return tenancyv1alpha1.WorkspaceInitializerLabelPrefix + hash[0:labelKeyHashLength], hash
}
