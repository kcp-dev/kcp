/*
Copyright 2025 The KCP Authors.

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

package termination

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// FinalizerForType determines the identifier for the implicit finalizer associated with the WorkspaceType.
func FinalizerForType(wt *tenancyv1alpha1.WorkspaceType) corev1alpha1.LogicalClusterFinalizer {
	return corev1alpha1.LogicalClusterFinalizer(logicalcluster.From(wt).Path().Join(wt.Name).String())
}

// TypeFrom determines the WorkspaceType workspace and name from an finalizer name.
func TypeFrom(finalizer corev1alpha1.LogicalClusterFinalizer) (logicalcluster.Name, string, error) {
	separatorIndex := strings.LastIndex(string(finalizer), ":")
	switch separatorIndex {
	case -1:
		return "", "", fmt.Errorf("expected workspace finalizer in form workspace:name, not %q", finalizer)
	default:
		return logicalcluster.Name(finalizer[:separatorIndex]), tenancyv1alpha1.ObjectName(tenancyv1alpha1.WorkspaceTypeName(finalizer[separatorIndex+1:])), nil
	}
}

// FinalizerToLabel transforms a finalizer into a key-value pair to add to a label set. We use a hash
// to create a unique identifier from this information, prefixing the hash in order to create a value which
// is unlikely to collide, and adding the full hash as a value in order to make it difficult to forge the pair.
func FinalizerToLabel(finalizer corev1alpha1.LogicalClusterFinalizer) (string, string) {
	hash := fmt.Sprintf("%x", sha256.Sum224([]byte(finalizer)))
	labelKeyHashLength := validation.LabelValueMaxLength - len(tenancyv1alpha1.WorkspaceFinalizerLabelPrefix)
	return tenancyv1alpha1.WorkspaceFinalizerLabelPrefix + hash[0:labelKeyHashLength], hash
}

// MergefinalizersUnique merges a list of finalizers with a list of strings. The
// result is ordered and all items are unique. It's main use is to create unique
// lists to be used in metadata.finalizers with existing finalizers.
func MergeFinalizersUnique(fin []corev1alpha1.LogicalClusterFinalizer, finString []string) []string {
	set := sets.New(finString...)
	for _, f := range fin {
		sets.Insert(set, FinalizerSpecToMetadata(f))
	}
	return sets.List(set)
}

// FinalizerSpecToMetadata converts a finalizer to a metadata.finalizer
// compatible string. Namely it escapes the ":" character.
func FinalizerSpecToMetadata(f corev1alpha1.LogicalClusterFinalizer) string {
	return strings.ReplaceAll(string(f), ":", ".")
}

// FinalizersToStrings converts a list of finalizers into a list of strings.
func FinalizersToStrings(finalizers []corev1alpha1.LogicalClusterFinalizer) []string {
	s := make([]string, 0, len(finalizers))
	for _, f := range finalizers {
		s = append(s, string(f))
	}
	return s
}
