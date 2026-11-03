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

package rootapiserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualcontext "github.com/kcp-dev/virtual-workspace-framework/pkg/context"
)

// resolverVW is a VirtualWorkspace with only a RootPathResolver.
type resolverVW struct {
	framework.VirtualWorkspace
	resolve framework.RootPathResolverFunc
}

func (v resolverVW) ResolveRootPath(ctx context.Context, match framework.PathMatch) (bool, string, context.Context) {
	return v.resolve(ctx, match)
}

func named(name string, resolve framework.RootPathResolverFunc, patterns ...string) NamedVirtualWorkspace {
	return NamedVirtualWorkspace{
		Name:             name,
		Patterns:         patterns,
		VirtualWorkspace: resolverVW{resolve: resolve},
	}
}

func acceptAll(ctx context.Context, match framework.PathMatch) (bool, string, context.Context) {
	return true, match.PrefixToStrip(), ctx
}

type claimed struct {
	called  bool
	path    string
	vw      string
	cluster *request.Cluster
}

func capture(got *claimed) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		vw, _ := virtualcontext.VirtualWorkspaceNameFrom(req.Context())
		*got = claimed{
			called:  true,
			path:    req.URL.Path,
			vw:      vw,
			cluster: request.ClusterFrom(req.Context()),
		}
	})
}

func TestPatternMux(t *testing.T) {
	t.Parallel()

	vws := []NamedVirtualWorkspace{
		named("apiexport", acceptAll, "/services/apiexport/{exportCluster}/{exportName}/clusters/{cluster}"),
		named("wildcardOnly", func(ctx context.Context, match framework.PathMatch) (bool, string, context.Context) {
			if !request.ClusterFrom(ctx).Wildcard {
				return false, "", ctx
			}
			return true, match.PrefixToStrip(), ctx
		}, "/services/init/{initializer}/clusters/{cluster}"),
		named("keepCluster", func(ctx context.Context, match framework.PathMatch) (bool, string, context.Context) {
			return true, "/services/init/" + match.Value("initializer"), ctx
		}, "/services/init/{initializer}/clusters/{cluster}"),
		named("admin", acceptAll, "/services/admin", "/services/admin/clusters/{cluster}"),
	}

	tests := map[string]struct {
		url         string
		wantVW      string
		wantPath    string
		wantCluster *request.Cluster
	}{
		"export named clusters": {
			url:         "/services/apiexport/root:org/clusters/clusters/abc/apis/v1/things",
			wantVW:      "apiexport",
			wantPath:    "/apis/v1/things",
			wantCluster: &request.Cluster{Name: "abc"},
		},
		"no trail": {
			url:         "/services/apiexport/root:org/export/clusters/abc",
			wantVW:      "apiexport",
			wantPath:    "",
			wantCluster: &request.Cluster{Name: "abc"},
		},
		"wildcard": {
			url:         "/services/apiexport/root:org/export/clusters/*/apis/v1",
			wantVW:      "apiexport",
			wantPath:    "/apis/v1",
			wantCluster: &request.Cluster{Wildcard: true},
		},
		"first accepting VW wins": {
			url:         "/services/init/foo/clusters/*/apis/v1",
			wantVW:      "wildcardOnly",
			wantPath:    "/apis/v1",
			wantCluster: &request.Cluster{Wildcard: true},
		},
		"later VW claims and keeps cluster prefix": {
			url:         "/services/init/foo/clusters/abc/apis/v1",
			wantVW:      "keepCluster",
			wantPath:    "/clusters/abc/apis/v1",
			wantCluster: &request.Cluster{Name: "abc"},
		},
		"clusterless pattern": {
			url:      "/services/admin/apis/v1",
			wantVW:   "admin",
			wantPath: "/apis/v1",
		},
		"clusterless pattern, bare clusters segment": {
			url:      "/services/admin/clusters",
			wantVW:   "admin",
			wantPath: "/clusters",
		},
		"cluster pattern wins over clusterless": {
			url:         "/services/admin/clusters/abc/apis/v1",
			wantVW:      "admin",
			wantPath:    "/apis/v1",
			wantCluster: &request.Cluster{Name: "abc"},
		},
		"unmatched passes unclaimed": {
			url:      "/services/other/clusters/abc/apis/v1",
			wantPath: "/services/other/clusters/abc/apis/v1",
		},
		"workspace path is not a cluster": {
			url:      "/services/apiexport/root:org/export/clusters/root:org/apis/v1",
			wantPath: "/services/apiexport/root:org/export/clusters/root:org/apis/v1",
		},
		"invalid cluster segment": {
			url:      "/services/apiexport/root:org/export/clusters/Bad!/apis/v1",
			wantPath: "/services/apiexport/root:org/export/clusters/Bad!/apis/v1",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got claimed
			mux := newPatternMux(vws, capture(&got))

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, http.NoBody))

			require.True(t, got.called)
			require.Equal(t, tc.wantVW, got.vw)
			require.Equal(t, tc.wantPath, got.path)
			require.Equal(t, tc.wantCluster, got.cluster)
		})
	}
}

func TestPatternMuxMatch(t *testing.T) {
	t.Parallel()

	var got framework.PathMatch
	vws := []NamedVirtualWorkspace{
		named("apiexport", func(ctx context.Context, match framework.PathMatch) (bool, string, context.Context) {
			got = match
			return true, match.PrefixToStrip(), ctx
		}, "/services/apiexport/{exportCluster}/{exportName}/clusters/{cluster}"),
	}
	mux := newPatternMux(vws, http.NotFoundHandler())

	url := "/services/apiexport/root:org/export/clusters/abc/apis/v1/things"
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody))

	require.Equal(t, url, got.Path)
	require.Equal(t, "apis/v1/things", got.Trail)
	require.Equal(t, "root:org", got.Value("exportCluster"))
	require.Equal(t, "export", got.Value("exportName"))
	require.Equal(t, "abc", got.Value("cluster"))
	require.Empty(t, got.Value("unknown"))
	require.Equal(t, "/services/apiexport/root:org/export/clusters/abc", got.PrefixToStrip())
}

func TestPatternMuxPanicsOnInvalidPattern(t *testing.T) {
	t.Parallel()

	vws := []NamedVirtualWorkspace{
		named("a", acceptAll, "/services/x/{a}/{a}/clusters/{cluster}"),
	}
	require.Panics(t, func() { newPatternMux(vws, http.NotFoundHandler()) })
}

func TestClusterFromSegment(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		segment string
		want    request.Cluster
		ok      bool
	}{
		"wildcard": {segment: "*", want: request.Cluster{Wildcard: true}, ok: true},
		"name":     {segment: "abc", want: request.Cluster{Name: logicalcluster.Name("abc")}, ok: true},
		"system":   {segment: "system:admin", want: request.Cluster{Name: logicalcluster.Name("system:admin")}, ok: true},
		"path":     {segment: "root:org"},
		"invalid":  {segment: "Bad!"},
		"empty":    {segment: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, ok := clusterFromSegment(tc.segment)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}
