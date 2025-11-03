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

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// TerminatorForType determines the identifier for the implicit terminator associated with the WorkspaceType.
func TerminatorForType(wt *tenancyv1alpha1.WorkspaceType) corev1alpha1.LogicalClusterTerminator {
	return corev1alpha1.LogicalClusterTerminator(logicalcluster.From(wt).Path().Join(wt.Name).String())
}

// TypeFrom determines the WorkspaceType workspace and name from an terminator name.
func TypeFrom(terminator corev1alpha1.LogicalClusterTerminator) (logicalcluster.Name, string, error) {
	separatorIndex := strings.LastIndex(string(terminator), ":")
	switch separatorIndex {
	case -1:
		return "", "", fmt.Errorf("expected workspace terminator in form workspace:name, not %q", terminator)
	default:
		return logicalcluster.Name(terminator[:separatorIndex]), tenancyv1alpha1.ObjectName(tenancyv1alpha1.WorkspaceTypeName(terminator[separatorIndex+1:])), nil
	}
}

// TerminatorToLabel transforms a terminator into a key-value pair to add to a label set. We use a hash
// to create a unique identifier from this information, prefixing the hash in order to create a value which
// is unlikely to collide, and adding the full hash as a value in order to make it difficult to forge the pair.
func TerminatorToLabel(terminator corev1alpha1.LogicalClusterTerminator) (string, string) {
	hash := fmt.Sprintf("%x", sha256.Sum224([]byte(terminator)))
	labelKeyHashLength := validation.LabelValueMaxLength - len(tenancyv1alpha1.WorkspaceTerminatorLabelPrefix)
	return tenancyv1alpha1.WorkspaceTerminatorLabelPrefix + hash[0:labelKeyHashLength], hash
}

// TerminatorsToStrings converts a list of terminators into a list of strings.
func TerminatorsToStrings(terminator []corev1alpha1.LogicalClusterTerminator) []string {
	s := make([]string, 0, len(terminator))
	for _, f := range terminator {
		s = append(s, string(f))
	}
	return s
}
