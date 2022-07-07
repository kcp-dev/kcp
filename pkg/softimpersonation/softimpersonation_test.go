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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
)

func TestSoftImpersonation(t *testing.T) {
	testCases := []struct {
		name                string
		userInfo            user.Info
		expectedCreationErr string
	}{
		{
			name: "valid user",
			userInfo: &user.DefaultInfo{
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
			name: "valid with nil groups",
			userInfo: &user.DefaultInfo{
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
			name: "valid with nil extra",
			userInfo: &user.DefaultInfo{
				Name:   "user name",
				UID:    "user uid",
				Groups: []string{"group1", "group2"},
				Extra:  nil,
			},
		},
		{
			name: "valid with nil extra values",
			userInfo: &user.DefaultInfo{
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
			userInfo:            nil,
			expectedCreationErr: "no user info",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var impersonatedUser user.Info
			var err error
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				impersonatedUser, err = UserInfoFromRequestHeader(r)
			}))
			defer server.Close()
			restConfig, err := clientcmd.BuildConfigFromFlags(server.URL, "")
			require.NoError(t, err)
			restConfig, err = WithSoftImpersonatedConfig(restConfig, testCase.userInfo)
			if testCase.expectedCreationErr != "" {
				require.EqualError(t, err, testCase.expectedCreationErr)
				return
			} else {
				require.NoError(t, err)
			}
			client, err := discovery.NewDiscoveryClientForConfig(restConfig)
			require.NoError(t, err)
			_, _ = client.ServerGroups()

			require.Equal(t, testCase.userInfo, impersonatedUser)
			require.Nil(t, err)
		})
	}
}
