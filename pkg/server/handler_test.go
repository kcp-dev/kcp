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

package server

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"
	"sigs.k8s.io/yaml"
)

func TestClusterWorkspaceNamePattern(t *testing.T) {
	_, fileName, _, _ := runtime.Caller(0)
	bs, err := ioutil.ReadFile(filepath.Join(filepath.Dir(fileName), "..", "..", "config/crds/tenancy.kcp.dev_clusterworkspaces.yaml"))
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
		{"foo:bar", true},
		{"foo:bar0", true},
		{"foo:bar-bar", true},
		{"foo:b123456789012345678901234567891", true},
		{"foo:b1234567890123456789012345678912", true},
		{"test-8827a131-f796-4473-8904-a0fa527696eb:b1234567890123456789012345678912", true},

		{"root:", false},
		{":root", false},
		{"root::foo", false},
		{"root:föö:bär", false},
		{"foo:bar_bar", false},
		{"foo:0", false},
		{"foo:0bar", false},
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

func TestProcessResourceIdentity(t *testing.T) {
	tests := map[string]struct {
		path             string
		expectError      bool
		expectedPath     string
		expectedResource string
		expectedIdentity string
	}{
		"/api - not a resource request": {
			path:         "/api",
			expectedPath: "/api",
		},
		"/apis - not a resource request": {
			path:         "/apis",
			expectedPath: "/apis",
		},
		"legacy - without identity - list all": {
			path:             "/api/v1/pods",
			expectedPath:     "/api/v1/pods",
			expectedResource: "pods",
		},
		"legacy - with identity - list all": {
			path:             "/api/v1/pods:abcd1234",
			expectedPath:     "/api/v1/pods",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
		"legacy - without identity - list namespace": {
			path:             "/api/v1/namespaces/default/pods",
			expectedPath:     "/api/v1/namespaces/default/pods",
			expectedResource: "pods",
		},
		"legacy - with identity - list namespace": {
			path:             "/api/v1/namespaces/default/pods:abcd1234",
			expectedPath:     "/api/v1/namespaces/default/pods",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
		"legacy - without identity - single pod": {
			path:             "/api/v1/namespaces/default/pods/foo",
			expectedPath:     "/api/v1/namespaces/default/pods/foo",
			expectedResource: "pods",
		},
		"legacy - with identity - single pod": {
			path:             "/api/v1/namespaces/default/pods:abcd1234/foo",
			expectedPath:     "/api/v1/namespaces/default/pods/foo",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
		"apis - without identity - list all": {
			path:             "/apis/somegroup.io/v1/pods",
			expectedPath:     "/apis/somegroup.io/v1/pods",
			expectedResource: "pods",
		},
		"apis - with identity - list all": {
			path:             "/apis/somegroup.io/v1/pods:abcd1234",
			expectedPath:     "/apis/somegroup.io/v1/pods",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
		"apis - without identity - list namespace": {
			path:             "/apis/somegroup.io/v1/namespaces/default/pods",
			expectedPath:     "/apis/somegroup.io/v1/namespaces/default/pods",
			expectedResource: "pods",
		},
		"apis - with identity - list namespace": {
			path:             "/apis/somegroup.io/v1/namespaces/default/pods:abcd1234",
			expectedPath:     "/apis/somegroup.io/v1/namespaces/default/pods",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
		"apis - without identity - single pod": {
			path:             "/apis/somegroup.io/v1/namespaces/default/pods/foo",
			expectedPath:     "/apis/somegroup.io/v1/namespaces/default/pods/foo",
			expectedResource: "pods",
		},
		"apis - with identity - single pod": {
			path:             "/apis/somegroup.io/v1/namespaces/default/pods:abcd1234/foo",
			expectedPath:     "/apis/somegroup.io/v1/namespaces/default/pods/foo",
			expectedResource: "pods",
			expectedIdentity: "abcd1234",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			url, err := url.Parse(test.path)
			require.NoError(t, err, "error parsing path %q to URL", test.path)

			checkRawPath := url.RawPath != ""

			req := &http.Request{
				URL:    url,
				Method: "GET",
			}

			requestInfoFactory := &request.RequestInfoFactory{
				APIPrefixes:          sets.NewString("api", "apis"),
				GrouplessAPIPrefixes: sets.NewString("api"),
			}

			requestInfo, err := requestInfoFactory.NewRequestInfo(req)
			require.NoError(t, err, "error creating requestInfo")

			req, err = processResourceIdentity(req, requestInfo)
			if test.expectError {
				require.Errorf(t, err, "expected error")
				return
			}

			require.NoError(t, err, "unexpected error")

			require.Equal(t, test.expectedPath, req.URL.Path, "unexpected req.URL.Path")
			if checkRawPath {
				require.Equal(t, test.expectedPath, req.URL.RawPath, "unexpected req.URL.RawPath")
			} else {
				require.Empty(t, req.URL.RawPath, "RawPath should be empty")
			}

			require.Equal(t, test.expectedResource, requestInfo.Resource, "unexpected requestInfo.Resource")

			identity := IdentityFromContext(req.Context())
			require.Equal(t, test.expectedIdentity, identity, "unexpected identity")
		})
	}
}
