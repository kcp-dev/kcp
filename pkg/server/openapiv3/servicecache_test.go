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

package openapiv3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
)

type erroringCRDClusterLister struct{}

func (erroringCRDClusterLister) Cluster(logicalcluster.Name) kcp.ClusterAwareCRDLister {
	return erroringCRDLister{}
}

type erroringCRDLister struct{}

func (erroringCRDLister) List(context.Context, labels.Selector) ([]*apiextensionsv1.CustomResourceDefinition, error) {
	return nil, errors.New("lister must not be reached for system:bound-crds")
}

func (erroringCRDLister) Get(context.Context, string) (*apiextensionsv1.CustomResourceDefinition, error) {
	return nil, errors.New("not implemented")
}

func (erroringCRDLister) Refresh(crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	return crd, nil
}

func TestServeHTTPSkipsSystemBoundCRDs(t *testing.T) {
	t.Parallel()

	c := NewServiceCache(nil, erroringCRDClusterLister{}, nil, DefaultServiceCacheSize)

	for _, tc := range []struct {
		name     string
		cluster  logicalcluster.Name
		wantCode int
	}{
		{name: "system:bound-crds serves an empty doc without listing CRDs", cluster: systemBoundCRDsClusterName, wantCode: http.StatusOK},
		{name: "regular workspace goes through the normal path", cluster: logicalcluster.Name("root:some-workspace"), wantCode: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tc.cluster})
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/openapi/v3", http.NoBody)

			rec := httptest.NewRecorder()
			c.ServeHTTP(rec, req)

			if rec.Code != tc.wantCode {
				t.Errorf("cluster %q: got status %d, want %d", tc.cluster, rec.Code, tc.wantCode)
			}
		})
	}
}
