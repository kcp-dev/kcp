/*
Copyright 2026 The kcp Authors.

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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
)

func TestWithOptionalAuthentication(t *testing.T) {
	t.Parallel()

	testcases := map[string]struct {
		auth         authenticator.Request
		expectedUser user.Info
	}{
		"authenticated": {
			auth: authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
				return &authenticator.Response{User: &user.DefaultInfo{Name: "alice"}}, true, nil
			}),
			expectedUser: &user.DefaultInfo{Name: "alice"},
		},
		"not authenticated": {
			auth: authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
				return nil, false, nil
			}),
		},
		"authentication error": {
			auth: authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
				return nil, false, errors.New("invalid bearer token")
			}),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var served bool
			var servedUser user.Info
			next := http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
				served = true
				servedUser, _ = request.UserFrom(req.Context())
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root/api", http.NoBody)
			req.Header.Set("Authorization", "Bearer token")

			WithOptionalAuthentication(next, tc.auth, true).ServeHTTP(httptest.NewRecorder(), req)

			require.True(t, served)
			require.Equal(t, tc.expectedUser, servedUser)
		})
	}
}
