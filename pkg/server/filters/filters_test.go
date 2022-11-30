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

package filters

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func Test_isPartialMetadataHeader(t *testing.T) {
	tests := map[string]struct {
		accept string
		want   bool
	}{
		"empty header": {
			accept: "",
			want:   false,
		},
		"metadata informer factory": {
			accept: "application/vnd.kubernetes.protobuf;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1,application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1,application/json",
			want:   true,
		},
	}
	for testName, test := range tests {
		t.Run(testName, func(t *testing.T) {
			got := isPartialMetadataHeader(test.accept)
			require.Equal(t, test.want, got)
		})
	}
}

func TestWorkspaceNamePattern(t *testing.T) {
	_, fileName, _, _ := runtime.Caller(0)
	bs, err := os.ReadFile(filepath.Join(filepath.Dir(fileName), "..", "..", "../config/crds/tenancy.kcp.dev_workspaces.yaml"))
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	err = yaml.Unmarshal(bs, &crd)
	require.NoError(t, err)
	require.Len(t, crd.Spec.Versions, 1, "crd should have exactly one version, update the test when this changes")

	namePattern := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["metadata"].Properties["name"].Pattern
	require.True(t, strings.HasPrefix(namePattern, "^"), "cluster name pattern should end with $")
	require.True(t, strings.HasSuffix(namePattern, "$"), "cluster name pattern should start with ^")

	namePattern = strings.Trim(namePattern, "^$")
	require.Equal(t, fmt.Sprintf("^(%s:)*%s$", namePattern, namePattern), reClusterName.String(), "logical cluster regex should match ClusterWorkspace name pattern")
}

func TestReCluster(t *testing.T) {
	tests := []struct {
		cluster string
		valid   bool
	}{
		{"", false},

		{"root", true},
		{"root:foo", true},
		{"root:foo:bar", true},

		{"system", true},
		{"system:foo", true},
		{"system:foo:bar", true},

		{"f", true},
		{"foo", true},
		{"foo:b", true},
		{"foo:0", true},
		{"foo:bar", true},
		{"foo:bar0", true},
		{"foo:0bar", true},
		{"foo:bar-bar", true},
		{"foo:b123456789012345678901234567891", true},
		{"foo:b1234567890123456789012345678912", true},
		{"test-8827a131-f796-4473-8904-a0fa527696eb:b1234567890123456789012345678912", true},

		{"root:", false},
		{":root", false},
		{"root::foo", false},
		{"root:föö:bär", false},
		{"foo:bar_bar", false},

		{"foo/bar", false},
		{"foo:bar-", false},
		{"foo:-bar", false},
		{"test-too-long-org-0020-4473-0030-a0fa-0040-5276-0050-sdg2-0060-w:b1234567890123456789012345678912", false},
	}
	for _, tt := range tests {
		t.Run(tt.cluster, func(t *testing.T) {
			if got := reClusterName.MatchString(tt.cluster); got != tt.valid {
				t.Errorf("reCluster.MatchString(%q) = %v, want %v", tt.cluster, got, tt.valid)
			}
		})
	}
}
