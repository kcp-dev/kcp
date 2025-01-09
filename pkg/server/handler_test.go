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
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
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
			u, err := url.Parse(test.path)
			require.NoError(t, err, "error parsing path %q to URL", test.path)

			checkRawPath := u.RawPath != ""

			req := &http.Request{
				URL:    u,
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

var _ user.Info = (*mockUser)(nil)

// mockUser represents a mock user for testing purposes.
type mockUser struct {
	Name   string
	UID    string
	Groups []string
	Extra  map[string][]string
}

func (u *mockUser) GetName() string {
	return u.Name
}

func (u *mockUser) GetUID() string {
	return u.UID
}

func (u *mockUser) GetGroups() []string {
	return u.Groups
}

func (u *mockUser) GetExtra() map[string][]string {
	return u.Extra
}

func TestWithImpersonationGatekeeper(t *testing.T) {
	// Define the special groups as per the original code.
	// These should match the groups defined in the main code for accurate privilege simulation.
	const (
		SystemMastersGroup  = authorizationbootstrap.SystemMastersGroup
		SystemKcpAdminGroup = authorizationbootstrap.SystemKcpAdminGroup
	)

	// Define test cases
	tests := []struct {
		name                   string
		impersonateUserHeader  string
		impersonateGroupHeader []string
		otherHeaders           map[string]string
		user                   user.Info
		expectedStatus         int
		handlerCalled          bool
	}{
		{
			name:                   "No impersonation headers",
			impersonateUserHeader:  "",
			impersonateGroupHeader: nil,
			user:                   nil,
			expectedStatus:         http.StatusOK,
			handlerCalled:          true,
		},
		{
			name:                   "Impersonation headers present, no user in context",
			impersonateUserHeader:  "impersonated-user",
			impersonateGroupHeader: []string{"group1"},
			user:                   nil,
			expectedStatus:         http.StatusForbidden,
			handlerCalled:          false,
		},
		{
			name:                   "Impersonation headers present, valid impersonation with normal group",
			impersonateUserHeader:  "impersonated-user",
			impersonateGroupHeader: []string{"group1"},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-123",
				Groups: []string{"group1", "group2"},
				Extra:  nil,
			},
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:                   "Impersonation headers present, invalid impersonation with higher privilege group",
			impersonateUserHeader:  "impersonated-user",
			impersonateGroupHeader: []string{SystemMastersGroup},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-456",
				Groups: []string{"group1", "group2"},
				Extra:  nil,
			},
			expectedStatus: http.StatusForbidden,
			handlerCalled:  false,
		},
		{
			name:                   "Impersonation headers present, valid impersonation with same privilege group",
			impersonateUserHeader:  "impersonated-user",
			impersonateGroupHeader: []string{SystemKcpAdminGroup},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-789",
				Groups: []string{SystemKcpAdminGroup, "group2"},
				Extra:  nil,
			},
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:                   "Impersonation headers present, invalid impersonation with higher privilege group",
			impersonateUserHeader:  "impersonated-user",
			impersonateGroupHeader: []string{SystemMastersGroup},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-101",
				Groups: []string{SystemKcpAdminGroup},
				Extra:  nil,
			},
			expectedStatus: http.StatusForbidden,
			handlerCalled:  false,
		},
		{
			name:                   "Impersonation headers present only for groups, invalid impersonation with higher privilege group",
			impersonateGroupHeader: []string{SystemMastersGroup},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-101",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			expectedStatus: http.StatusForbidden,
			handlerCalled:  false,
		},
		{
			name:                   "Impersonation headers present only for groups, valid impersonation with random group",
			impersonateGroupHeader: []string{"group2"},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-101",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
		{
			name:                   "Impersonation headers present only for groups, invalid impersonation with privileged group",
			impersonateGroupHeader: []string{SystemKcpAdminGroup},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-101",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			expectedStatus: http.StatusForbidden,
			handlerCalled:  false,
		},
		{
			name: "Impersonation headers present only for extras, valid impersonation",
			otherHeaders: map[string]string{
				"Impersonate-Extra-Super-Secure-data": "bob",
			},
			user: &mockUser{
				Name:   "requester",
				UID:    "uid-101",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			expectedStatus: http.StatusOK,
			handlerCalled:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlerCalledFlag := false

			// Create a mock handler that sets the flag when called
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalledFlag = true
				w.WriteHeader(http.StatusOK)
			})

			// Wrap the handler with WithImpersonationGatekeeper
			wrappedHandler := WithImpersonationGatekeeper(mockHandler)

			// Create a new HTTP request
			req := httptest.NewRequest(http.MethodGet, "http://kcp.io/foo", http.NoBody)

			// Set impersonation headers if present
			if tt.impersonateUserHeader != "" {
				req.Header.Set(authenticationv1.ImpersonateUserHeader, tt.impersonateUserHeader)
			}
			for _, group := range tt.impersonateGroupHeader {
				req.Header.Add(authenticationv1.ImpersonateGroupHeader, group)
			}
			if tt.otherHeaders != nil {
				for key, value := range tt.otherHeaders {
					req.Header.Set(key, value)
				}
			}

			// Set up context with user if provided
			ctx := req.Context()
			if tt.user != nil {
				ctx = request.WithUser(ctx, tt.user)
			}
			req = req.WithContext(ctx)

			// Create a ResponseRecorder to capture the response
			rr := httptest.NewRecorder()

			// Serve the HTTP request
			wrappedHandler.ServeHTTP(rr, req)

			// Assert the expected status code
			assert.Equal(t, tt.expectedStatus, rr.Code, "Unexpected status code")

			// Assert whether the handler was called
			assert.Equal(t, tt.handlerCalled, handlerCalledFlag, "Handler called state mismatch")
		})
	}
}
