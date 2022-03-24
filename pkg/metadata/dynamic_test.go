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

package metadata

import (
	"net/http"
	url2 "net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartialType(t *testing.T) {
	tests := []struct {
		method  string
		url     string
		want    string
		wantErr bool
	}{
		{"GET", "/clusters/*/apis/apps/v1/deployments", "PartialObjectMetadataList", false},
		{"GET", "/clusters/*/apis/apps/v1/deployments?watch=true", "PartialObjectMetadata", false},
		{"GET", "/clusters/*/apis/apps/v1/deployments/foo", "PartialObjectMetadata", false},
		{"DELETE", "/clusters/*/apis/apps/v1/deployments/foo", "", true},
		{"POST", "/clusters/*/apis/apps/v1/deployments/foo", "", true},
		{"PUT", "/clusters/*/apis/apps/v1/deployments/foo", "", true},

		{"GET", "/clusters/*/api/v1/configmaps", "PartialObjectMetadataList", false},
		{"GET", "/clusters/*/api/v1/configmaps?watch=true", "PartialObjectMetadata", false},
		{"GET", "/clusters/*/api/v1/configmaps/foo", "PartialObjectMetadata", false},
		{"DELETE", "/clusters/*/api/v1/configmaps/foo", "", true},
		{"POST", "/clusters/*/api/v1/configmaps/foo", "", true},
		{"PUT", "/clusters/*/api/v1/configmaps/foo", "", true},

		{"GET", "/clusters/root/apis/apps/v1/deployments", "PartialObjectMetadataList", false},
		{"GET", "/clusters/root/apis/apps/v1/deployments?watch=true", "PartialObjectMetadata", false},
		{"GET", "/clusters/root/apis/apps/v1/deployments/foo", "PartialObjectMetadata", false},
		{"DELETE", "/clusters/root/apis/apps/v1/deployments/foo", "", true},
		{"POST", "/clusters/root/apis/apps/v1/deployments/foo", "", true},
		{"PUT", "/clusters/root/apis/apps/v1/deployments/foo", "", true},

		{"GET", "/apis/apps/v1/deployments", "PartialObjectMetadataList", false},
		{"GET", "/apis/apps/v1/deployments?watch=true", "PartialObjectMetadata", false},
		{"GET", "/apis/apps/v1/deployments/foo", "PartialObjectMetadata", false},
		{"DELETE", "/apis/apps/v1/deployments/foo", "", true},
		{"POST", "/apis/apps/v1/deployments/foo", "", true},
		{"PUT", "/apis/apps/v1/deployments/foo", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.url, func(t *testing.T) {
			url, err := url2.Parse(tt.url)
			require.NoError(t, err)

			req := &http.Request{
				Method: tt.method,
				URL:    url,
			}

			got, err := partialType(req)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}
