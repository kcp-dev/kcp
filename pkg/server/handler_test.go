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
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/request"

	authorizationbootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

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
				Method: http.MethodGet,
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

func TestCheckImpersonation(t *testing.T) {
	var systemUserGroup = "system:user:group"
	nonExistingGroup := "non-existing-group"
	specialGroups = map[string]privilege{
		authorizationbootstrap.SystemMastersGroup:  superPrivileged,
		authorizationbootstrap.SystemKcpAdminGroup: priviledged,
		systemUserGroup: unprivileged,
	}

	tests := []struct {
		name            string
		userGroups      []string
		requestedGroups []string
		expectedResult  bool
	}{
		{
			name:            "Single group - allowed",
			userGroups:      []string{authorizationbootstrap.SystemMastersGroup},
			requestedGroups: []string{authorizationbootstrap.SystemKcpAdminGroup},
			expectedResult:  true,
		},
		{
			name:            "Multiple groups - allowed",
			userGroups:      []string{authorizationbootstrap.SystemMastersGroup},
			requestedGroups: []string{authorizationbootstrap.SystemKcpAdminGroup, systemUserGroup},
			expectedResult:  true,
		},
		{
			name:            "Single group - not allowed",
			userGroups:      []string{authorizationbootstrap.SystemKcpAdminGroup},
			requestedGroups: []string{authorizationbootstrap.SystemMastersGroup},
			expectedResult:  false,
		},
		{
			name:            "Multiple groups - mixed permissions",
			userGroups:      []string{authorizationbootstrap.SystemKcpAdminGroup},
			requestedGroups: []string{authorizationbootstrap.SystemMastersGroup, systemUserGroup},
			expectedResult:  false,
		},
		{
			name:            "Multiple groups - lower permissions only",
			userGroups:      []string{systemUserGroup},
			requestedGroups: []string{authorizationbootstrap.SystemKcpAdminGroup, authorizationbootstrap.SystemMastersGroup},
			expectedResult:  false,
		},
		{
			name:            "Empty user groups",
			userGroups:      []string{},
			requestedGroups: []string{authorizationbootstrap.SystemKcpAdminGroup},
			expectedResult:  false,
		},
		{
			name:            "Empty requested groups",
			userGroups:      []string{authorizationbootstrap.SystemMastersGroup},
			requestedGroups: []string{},
			expectedResult:  true,
		},
		{
			name:            "Unknown requested group",
			userGroups:      []string{authorizationbootstrap.SystemMastersGroup},
			requestedGroups: []string{nonExistingGroup},
			expectedResult:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := validImpersonation(tt.userGroups, tt.requestedGroups)
			if result != tt.expectedResult {
				t.Errorf("checkImpersonation(%v, %v) = %v; want %v",
					tt.userGroups, tt.requestedGroups, result, tt.expectedResult)
			}
		})
	}
}
