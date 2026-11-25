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

package authentication

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	virtualcontext "github.com/kcp-dev/virtual-workspace-framework/pkg/context"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy"
)

type fakeAuthIndex map[string]authenticator.Request

func (f fakeAuthIndex) Lookup(wsType logicalcluster.Path) (authenticator.Request, bool) {
	authn, ok := f[wsType.String()]
	return authn, ok
}

func userAuthenticator(name string) authenticator.Request {
	return authenticator.RequestFunc(func(*http.Request) (*authenticator.Response, bool, error) {
		return &authenticator.Response{User: &user.DefaultInfo{Name: name}}, true, nil
	})
}

func TestWithWorkspaceAuthResolverAPIExport(t *testing.T) {
	t.Parallel()

	providerType := logicalcluster.NewPath("root:provider-x")
	consumerType := logicalcluster.NewPath("root:org")

	tests := []struct {
		name      string
		authIndex fakeAuthIndex
		export    *proxy.ExportWorkspace
		// type and cluster resolved from the /clusters/<cluster> part of the URL
		pathType    logicalcluster.Path
		pathCluster logicalcluster.Name

		expectedUser  string
		expectedScope string
	}{
		{
			name:          "export with workspace auth wins over the path cluster",
			authIndex:     fakeAuthIndex{providerType.String(): userAuthenticator("provider"), consumerType.String(): userAuthenticator("consumer")},
			export:        &proxy.ExportWorkspace{Cluster: "exportcluster", Type: providerType},
			pathType:      consumerType,
			pathCluster:   "consumercluster",
			expectedUser:  "provider",
			expectedScope: "cluster:exportcluster",
		},
		{
			name:          "export with workspace auth and wildcard request",
			authIndex:     fakeAuthIndex{providerType.String(): userAuthenticator("provider")},
			export:        &proxy.ExportWorkspace{Cluster: "exportcluster", Type: providerType},
			expectedUser:  "provider",
			expectedScope: "cluster:exportcluster",
		},
		{
			name:          "export without workspace auth keeps the old behavior",
			authIndex:     fakeAuthIndex{consumerType.String(): userAuthenticator("consumer")},
			export:        &proxy.ExportWorkspace{Cluster: "exportcluster", Type: providerType},
			pathType:      consumerType,
			pathCluster:   "consumercluster",
			expectedUser:  "consumer",
			expectedScope: "cluster:consumercluster",
		},
		{
			name:      "export without workspace auth and wildcard request has no authenticator",
			authIndex: fakeAuthIndex{},
			export:    &proxy.ExportWorkspace{Cluster: "exportcluster", Type: providerType},
		},
		{
			name:          "no export keeps the old behavior",
			authIndex:     fakeAuthIndex{consumerType.String(): userAuthenticator("consumer")},
			pathType:      consumerType,
			pathCluster:   "consumercluster",
			expectedUser:  "consumer",
			expectedScope: "cluster:consumercluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/services/apiexport/exportcluster/my.export/clusters/*/apis", nil)
			ctx := req.Context()
			if tt.export != nil {
				ctx = proxy.WithExportWorkspace(ctx, *tt.export)
			}
			if !tt.pathType.Empty() {
				ctx = proxy.WithWorkspaceType(ctx, tt.pathType)
				ctx = request.WithCluster(ctx, request.Cluster{Name: tt.pathCluster})
			}
			req = req.WithContext(ctx)

			var got *http.Request
			WithWorkspaceAuthResolver(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r }), tt.authIndex).
				ServeHTTP(httptest.NewRecorder(), req)

			authn, ok := WorkspaceAuthenticatorFrom(got.Context())
			if tt.expectedUser == "" {
				if ok {
					t.Fatalf("expected no workspace authenticator")
				}
				return
			}
			if !ok {
				t.Fatalf("expected a workspace authenticator")
			}

			resp, authenticated, err := authn.AuthenticateRequest(got)
			if err != nil || !authenticated {
				t.Fatalf("expected request to authenticate, got authenticated=%v err=%v", authenticated, err)
			}
			if resp.User.GetName() != tt.expectedUser {
				t.Errorf("expected user %q, got %q", tt.expectedUser, resp.User.GetName())
			}
			if scopes := resp.User.GetExtra()["authentication.kcp.io/scopes"]; !slices.Contains(scopes, tt.expectedScope) {
				t.Errorf("expected scope %q, got %v", tt.expectedScope, scopes)
			}
		})
	}
}

func TestAPIExportWorkspaceAuthenticator(t *testing.T) {
	t.Parallel()

	clusterIndex := index.New(nil)
	clusterIndex.UpsertShard("root", "https://root")
	for cluster, wsType := range map[string]string{"exportcluster": "root:provider-x", "plaincluster": "root:provider"} {
		clusterIndex.UpsertLogicalCluster("root", &corev1alpha1.LogicalCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster", Annotations: map[string]string{
				"kcp.io/cluster": cluster,
				tenancyv1alpha1.LogicalClusterTypeAnnotationKey: wsType,
			}},
		})
	}
	authIndex := fakeAuthIndex{"root:provider-x": userAuthenticator("provider")}

	tests := []struct {
		name          string
		vwName        string
		domainKey     string
		expectedUser  string
		expectedScope string
	}{
		{
			name:          "export with workspace auth",
			vwName:        "apiexport",
			domainKey:     "exportcluster/my.export",
			expectedUser:  "provider",
			expectedScope: "cluster:exportcluster",
		},
		{name: "export without workspace auth", vwName: "apiexport", domainKey: "plaincluster/my.export"},
		{name: "unknown export cluster", vwName: "apiexport", domainKey: "unknown/my.export"},
		{name: "other virtual workspace", vwName: "replication", domainKey: "exportcluster/my.export"},
		{name: "no virtual workspace", domainKey: "exportcluster/my.export"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := dynamiccontext.WithAPIDomainKey(t.Context(), dynamiccontext.APIDomainKey(tt.domainKey))
			if tt.vwName != "" {
				ctx = virtualcontext.WithVirtualWorkspaceName(ctx, tt.vwName)
			}
			req := httptest.NewRequest(http.MethodGet, "/clusters/*/apis", nil).WithContext(ctx)

			resp, authenticated, err := NewAPIExportWorkspaceAuthenticator(clusterIndex, authIndex).AuthenticateRequest(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.expectedUser == "" {
				if authenticated {
					t.Fatalf("expected no opinion, got user %q", resp.User.GetName())
				}
				return
			}
			if !authenticated {
				t.Fatalf("expected request to authenticate")
			}
			if resp.User.GetName() != tt.expectedUser {
				t.Errorf("expected user %q, got %q", tt.expectedUser, resp.User.GetName())
			}
			if scopes := resp.User.GetExtra()["authentication.kcp.io/scopes"]; !slices.Contains(scopes, tt.expectedScope) {
				t.Errorf("expected scope %q, got %v", tt.expectedScope, scopes)
			}
		})
	}
}
