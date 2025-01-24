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

package filters

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"

	authorizationbootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestValidImpersonation(t *testing.T) {
	var systemUserGroup = "system:user:group"
	nonExistingGroup := "non-existing-group"

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
			name:            "system:authenticated is beyond unprivileged",
			userGroups:      []string{systemUserGroup},
			requestedGroups: []string{user.AllAuthenticated},
			expectedResult:  false,
		},
		{
			name:            "system:autenticated can impersonate itself",
			userGroups:      []string{systemUserGroup, user.AllAuthenticated},
			requestedGroups: []string{user.AllAuthenticated},
			expectedResult:  true,
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
		{
			name:            "user is in requesting 'system:authenticated' group without having 'system:authenticated' group",
			userGroups:      []string{"bob"},
			requestedGroups: []string{user.AllAuthenticated},
			expectedResult:  false,
		},
		{
			name:            "user is in requesting 'system:authenticated' group with 'system:authenticated' group",
			userGroups:      []string{"bob", user.AllAuthenticated},
			requestedGroups: []string{user.AllAuthenticated},
			expectedResult:  true,
		},
		{
			name:            "user is in requesting 'system:authenticated' group with priviledged group",
			userGroups:      []string{"bob", authorizationbootstrap.SystemMastersGroup},
			requestedGroups: []string{user.AllAuthenticated},
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
		name                     string
		impersonateUserHeader    string
		impersonateGroupHeader   []string
		otherHeaders             map[string]string
		user                     user.Info
		expectedStatus           int
		expectedImpersonationKey bool
		expectedOriginalUser     bool
		handlerCalled            bool
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
			expectedStatus:           http.StatusOK,
			expectedImpersonationKey: true,
			expectedOriginalUser:     true,
			handlerCalled:            true,
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
			expectedStatus:           http.StatusOK,
			expectedImpersonationKey: true,
			expectedOriginalUser:     true,
			handlerCalled:            true,
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
			expectedStatus:           http.StatusOK,
			expectedImpersonationKey: true,
			expectedOriginalUser:     true,
			handlerCalled:            true,
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
			expectedStatus:           http.StatusOK,
			expectedImpersonationKey: true,
			expectedOriginalUser:     true,
			handlerCalled:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlerCalledFlag := false

			// Create a mock handler that sets the flag when called
			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()

				impersonationFlag, ok := ctx.Value(impersonationContextKey).(bool)
				assert.True(t, ok, "context should always contain impersonation context key")
				assert.Equal(t, tt.expectedImpersonationKey, impersonationFlag, "impersonation context key should match expected value")

				if tt.expectedOriginalUser {
					user, ok := ctx.Value(originalUserContextKey).(user.Info)
					assert.True(t, ok, "context should include original user (impersonation requestor)")
					assert.Equal(t, tt.user, user, "user stored in context should equal requesting user")
				}

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

// TestWithScoping tests the WithScoping middleware.
func TestWithScoping(t *testing.T) {
	tests := []struct {
		name               string
		originalUser       user.Info
		impersonatedUser   user.Info
		cluster            *request.Cluster
		expectedStatus     int
		handlerCalled      bool
		expectedExtraScope string
		noImpersonationKey bool
	}{
		{
			name:               "no scoping marker. Programmers error.",
			originalUser:       &mockUser{Name: "original-user"},
			impersonatedUser:   &mockUser{Name: "test-user", UID: "uid-123", Groups: []string{"group1"}, Extra: nil},
			cluster:            &request.Cluster{Name: "cluster-1"},
			expectedStatus:     http.StatusInternalServerError,
			handlerCalled:      false,
			noImpersonationKey: true,
		},
		{
			name:             "No user in context",
			originalUser:     &mockUser{Name: "original-user"},
			impersonatedUser: nil,
			cluster:          &request.Cluster{Name: "cluster-1"},
			expectedStatus:   http.StatusInternalServerError,
			handlerCalled:    false,
		},
		{
			name:         "No cluster in context",
			originalUser: &mockUser{Name: "original-user"},
			impersonatedUser: &mockUser{
				Name:   "test-user",
				UID:    "uid-123",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			expectedStatus: http.StatusInternalServerError,
			handlerCalled:  false,
		},
		{
			name:         "Valid user and cluster with no existing extra scopes",
			originalUser: &mockUser{Name: "original-user"},
			impersonatedUser: &mockUser{
				Name:   "test-user",
				UID:    "uid-456",
				Groups: []string{"group1"},
				Extra:  nil,
			},
			cluster:            &request.Cluster{Name: "cluster-2"},
			expectedStatus:     http.StatusOK,
			handlerCalled:      true,
			expectedExtraScope: "cluster:cluster-2",
		},
		{
			name:         "Valid user and cluster with existing extra scopes",
			originalUser: &mockUser{Name: "original-user"},
			impersonatedUser: &mockUser{
				Name:   "test-user",
				UID:    "uid-789",
				Groups: []string{"group1"},
				Extra: map[string][]string{
					"existing.key": {"value1"},
				},
			},
			cluster:            &request.Cluster{Name: "cluster-3"},
			expectedStatus:     http.StatusOK,
			handlerCalled:      true,
			expectedExtraScope: "cluster:cluster-3",
		},
		{
			name:         "Original user was part of system:masters group",
			originalUser: &mockUser{Name: "original-user", Groups: []string{"system:masters"}},
			impersonatedUser: &mockUser{
				Name: "test-user",
				UID:  "uid-789",
			},
			cluster:            &request.Cluster{Name: "cluster-4"},
			expectedStatus:     http.StatusOK,
			handlerCalled:      true,
			expectedExtraScope: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false

			// Create a mock handler that sets the flag when called
			mock := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true

				// Retrieve the user from the context to verify extra scopes
				u, exists := request.UserFrom(r.Context())
				if !exists {
					t.Errorf("Expected user in context, but not found")
					responsewriters.InternalError(w, r, fmt.Errorf("no user in context"))
					return
				}

				// Verify that the extra scope is correctly added
				extra := u.GetExtra()
				expectedScope := tt.expectedExtraScope
				if expectedScope != "" {
					scopes, ok := extra["authentication.kcp.io/scopes"]
					assert.True(t, ok, "Expected 'authentication.kcp.io/scopes' in user extra")
					assert.Contains(t, scopes, expectedScope, "Expected scope not found in user extra")
				}

				w.WriteHeader(http.StatusOK)
			})
			wrapped := WithImpersonationScoping(mock)

			// prepare the request
			req := httptest.NewRequest(http.MethodGet, "http://kcp.io/foo", http.NoBody)
			ctx := req.Context()
			if !tt.noImpersonationKey {
				ctx = context.WithValue(ctx, impersonationContextKey, true)
				ctx = context.WithValue(ctx, originalUserContextKey, tt.originalUser)
			}
			if tt.impersonatedUser != nil {
				ctx = request.WithUser(ctx, tt.impersonatedUser)
			}
			if tt.cluster != nil {
				ctx = request.WithCluster(ctx, *tt.cluster)
			}
			req = req.WithContext(ctx)

			// send the request
			rr := httptest.NewRecorder()
			wrapped.ServeHTTP(rr, req)

			// Assert the expected status code
			assert.Equal(t, tt.expectedStatus, rr.Code, "Unexpected status code")
			assert.Equal(t, tt.handlerCalled, called, "Handler called state mismatch")
		})
	}
}
