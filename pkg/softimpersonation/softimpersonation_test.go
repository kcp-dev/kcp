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

package softimpersonation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
)

func TestSoftImpersonation(t *testing.T) {
	testCases := []struct {
		name                 string
		currentUserGroups    []string
		userToImpersonate    kuser.Info
		expectedCreationErr  string
		expectedRetrievalErr string
	}{
		{
			name:              "valid user",
			currentUserGroups: []string{kuser.SystemPrivilegedGroup},
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra: map[string][]string{
					"extra1": {"val11", "val12"},
					"extra2": {"val21", "val22"},
				},
			},
		},
		{
			name:              "valid with nil groups",
			currentUserGroups: []string{kuser.SystemPrivilegedGroup},
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: nil,
				Extra: map[string][]string{
					"extra1": {"val11", "val12"},
					"extra2": {"val21", "val22"},
				},
			},
		},
		{
			name:              "valid with nil extra",
			currentUserGroups: []string{kuser.SystemPrivilegedGroup},
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra:  nil,
			},
		},
		{
			name:              "valid with nil extra values",
			currentUserGroups: []string{kuser.SystemPrivilegedGroup},
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra: map[string][]string{
					"extra1": nil,
				},
			},
		},
		{
			name:                "nil user",
			currentUserGroups:   []string{kuser.SystemPrivilegedGroup},
			userToImpersonate:   nil,
			expectedCreationErr: "no user info",
		},
		{
			name:              "fail if current user is not privileged system user",
			currentUserGroups: []string{"other-group"},
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra: map[string][]string{
					"extra1": {"val11", "val12"},
					"extra2": {"val21", "val22"},
				},
			},
			expectedRetrievalErr: "soft impersonation not allowed",
		},
		{
			name:              "fail if no current user",
			currentUserGroups: nil,
			userToImpersonate: &kuser.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra: map[string][]string{
					"extra1": {"val11", "val12"},
					"extra2": {"val21", "val22"},
				},
			},
			expectedRetrievalErr: "permission to do soft impersonation could not be checked",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var impersonatedUser kuser.Info
			var retrievalError error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if testCase.currentUserGroups != nil {
					r = r.WithContext(request.WithUser(context.TODO(), &kuser.DefaultInfo{Name: "aName", Groups: testCase.currentUserGroups}))
				}
				impersonatedUser, retrievalError = UserInfoFromRequestHeader(r)
			}))
			defer server.Close()
			restConfig, err := clientcmd.BuildConfigFromFlags(server.URL, "")
			require.NoError(t, err)
			restConfig, err = WithSoftImpersonatedConfig(restConfig, testCase.userToImpersonate)
			if testCase.expectedCreationErr != "" {
				require.EqualError(t, err, testCase.expectedCreationErr)
				return
			} else {
				require.NoError(t, err)
			}
			client, err := discovery.NewDiscoveryClientForConfig(restConfig)
			require.NoError(t, err)
			_, _ = client.ServerGroups()

			if testCase.expectedRetrievalErr == "" {
				require.Equal(t, testCase.userToImpersonate, impersonatedUser)
				require.Nil(t, retrievalError)
			} else {
				require.Nil(t, impersonatedUser)
				require.EqualError(t, retrievalError, testCase.expectedRetrievalErr)
			}
		})
	}
}
