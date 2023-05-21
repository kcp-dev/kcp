/*
Copyright 2023 The KCP Authors.

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

package requestinfo

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestInfoResolver(t *testing.T) {
	tests := map[string]struct {
		path                string
		expectedPath        string
		expectedResource    string
		expectedSubresource string
	}{
		"path with /api prefix": {
			path:             "/api/v1/namespaces/default/pods",
			expectedPath:     "/api/v1/namespaces/default/pods",
			expectedResource: "pods",
		},
		"path with /apis prefix": {
			path:             "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody",
			expectedPath:     "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody",
			expectedResource: "cowboys",
		},
		"path with /apis prefix -- status subresource": {
			path:                "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody/status",
			expectedPath:        "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody/status",
			expectedResource:    "cowboys",
			expectedSubresource: "status",
		},
		"path with /apis prefix -- clusters resource": {
			path:             "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody",
			expectedPath:     "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody",
			expectedResource: "clusters",
		},
		"path with /apis prefix -- clusters resource, status subresource": {
			path:                "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedPath:        "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedResource:    "clusters",
			expectedSubresource: "status",
		},
		"path with /clusters prefix": {
			path:             "/clusters/root/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody",
			expectedPath:     "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody",
			expectedResource: "cowboys",
		},
		"path with /clusters prefix -- status subresource": {
			path:                "/clusters/root/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody/status",
			expectedPath:        "/apis/wildwest.dev/v1alpha1/namespaces/default/cowboys/woody/status",
			expectedResource:    "cowboys",
			expectedSubresource: "status",
		},
		"path with /clusters prefix -- clusters resource": {
			path:             "/clusters/root/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody",
			expectedPath:     "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody",
			expectedResource: "clusters",
		},
		"path with /clusters prefix -- clusters resource, status subresource": {
			path:                "/clusters/root:my-ws/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedPath:        "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedResource:    "clusters",
			expectedSubresource: "status",
		},
		"path with /services prefix": {
			path:             "/services/apiexport/root:my-ws/my-service/clusters/*/api/v1/configmaps",
			expectedPath:     "/api/v1/configmaps",
			expectedResource: "configmaps",
		},
		"path with /services prefix -- clusters resource, status subresource": {
			path:                "/services/apiexport/root:my-ws/my-service/clusters/*/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedPath:        "/apis/wildwest.dev/v1alpha1/namespaces/default/clusters/woody/status",
			expectedResource:    "clusters",
			expectedSubresource: "status",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			url, err := url.Parse(test.path)
			require.NoError(t, err, "error parsing path %q to URL", test.path)

			req := &http.Request{
				URL:    url,
				Method: http.MethodGet,
			}

			requestInfo, err := NewKCPRequestInfoResolver().NewRequestInfo(req)
			require.NoError(t, err, "unexpected error")

			require.Equal(t, test.expectedPath, requestInfo.Path, "unexpected requestInfo.Path")
			require.Equal(t, test.expectedResource, requestInfo.Resource, "unexpected requestInfo.Resource")
			require.Equal(t, test.expectedSubresource, requestInfo.Subresource, "unexpected requestInfo.Subresource")
		})
	}
}
