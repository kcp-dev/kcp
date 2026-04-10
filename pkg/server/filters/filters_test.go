/*
Copyright 2022 The kcp Authors.

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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
)

var (
	// reClusterName is a regular expression for cluster names. It is based on
	// modified RFC 1123. It allows for 63 characters for single name and includes
	// kcp specific ':' separator for workspace nesting. We are not re-using k8s
	// validation regex because its purpose is for single name validation.
	reClusterName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?:)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
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
	bs, err := os.ReadFile(filepath.Join(filepath.Dir(fileName), "..", "..", "..", "config", "crds", "tenancy.kcp.io_workspaces.yaml"))
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	err = yaml.Unmarshal(bs, &crd)
	require.NoError(t, err)
	require.Len(t, crd.Spec.Versions, 1, "crd should have exactly one version, update the test when this changes")

	namePattern := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["metadata"].Properties["name"].Pattern
	require.True(t, strings.HasPrefix(namePattern, "^"), "cluster name pattern should end with $")
	require.True(t, strings.HasSuffix(namePattern, "$"), "cluster name pattern should start with ^")

	namePattern = strings.Trim(namePattern, "^$")
	require.Equal(t, fmt.Sprintf("^(%s:)*%s$", namePattern, namePattern), reClusterName.String(), "logical cluster regex should match Workspace name pattern")
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

// TestWithClusterNameShapeInvariant verifies that the defense-in-depth filter
// rejects requests whose context carries a path-shaped cluster name (which
// would otherwise be concatenated verbatim into the etcd key by
// NoNamespaceKeyRootFunc, producing orphaned rows), while leaving legitimate
// bare names, system:* names, wildcard requests, and unset-cluster requests
// alone.
func TestWithClusterNameShapeInvariant(t *testing.T) {
	tests := map[string]struct {
		cluster    *request.Cluster
		wantStatus int
		wantCalled bool
	}{
		"no cluster on context": {
			cluster:    nil,
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"bare logical cluster name (hash)": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("25t3xbr5iceb0155")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"bare human-readable name": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("root")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"system:admin is allowed": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("system:admin")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"system:system-crds is allowed": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("system:system-crds")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"wildcard request passes through even with weird name": {
			cluster:    &request.Cluster{Wildcard: true, Name: logicalcluster.Name("*")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"empty name passes through": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("")},
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		"path-shaped name root:internal-cluster is rejected": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("root:internal-cluster")},
			wantStatus: http.StatusInternalServerError,
			wantCalled: false,
		},
		"path-shaped name root:org:ws is rejected": {
			cluster:    &request.Cluster{Name: logicalcluster.Name("root:org:ws")},
			wantStatus: http.StatusInternalServerError,
			wantCalled: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			called := false
			downstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			h := WithClusterNameShapeInvariant(downstream)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/default/configmaps", http.NoBody)
			if tt.cluster != nil {
				req = req.WithContext(request.WithCluster(req.Context(), *tt.cluster))
			}
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, req)

			require.Equal(t, tt.wantStatus, rr.Code, "status code; body=%s", rr.Body.String())
			require.Equal(t, tt.wantCalled, called, "downstream called")
		})
	}
}
