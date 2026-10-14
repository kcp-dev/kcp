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

package lookup

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
)

const (
	rootShard = "root"
	betaShard = "beta"

	rootClusterID = "root"
	orgClusterID  = "34"
	wsClusterID   = "56"
	downClusterID = "78"

	orgWorkspacePath  = "root:org"
	wsWorkspacePath   = "root:org:ws"
	mntWorkspacePath  = "root:org:mnt"
	downWorkspacePath = "root:org:down"

	mntURL = "https://mount.example.com"
)

// Index graph:
//
//	root (shard root)
//	└── org (cluster 34, shard root, type root:organization)
//	    ├── ws (cluster 56, shard beta, type root:universal)
//	    ├── mnt (mount https://mount.example.com)
//	    └── down (cluster 78, shard root, 503)
func newResolveIndex() *index.State {
	idx := index.New(nil)
	idx.UpsertShard(rootShard, "https://root.example.com")
	idx.UpsertShard(betaShard, "https://beta.example.com")

	idx.UpsertLogicalCluster(rootShard, newLogicalCluster(rootClusterID, ""))
	idx.UpsertWorkspace(rootShard, newWorkspace("org", rootClusterID, orgClusterID))
	idx.UpsertLogicalCluster(rootShard, newLogicalCluster(orgClusterID, "root:organization"))
	idx.UpsertWorkspace(rootShard, newWorkspace("ws", orgClusterID, wsClusterID))
	idx.UpsertLogicalCluster(betaShard, newLogicalCluster(wsClusterID, "root:universal"))

	mnt := newWorkspace("mnt", orgClusterID, "")
	mnt.Spec.Mount = &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{Kind: "Thing", Name: "x"}}
	mnt.Spec.URL = mntURL
	idx.UpsertWorkspace(rootShard, mnt)

	down := newWorkspace("down", orgClusterID, downClusterID)
	down.Status.Phase = corev1alpha1.LogicalClusterPhaseUnavailable
	idx.UpsertWorkspace(rootShard, down)
	idx.UpsertLogicalCluster(rootShard, newLogicalCluster(downClusterID, "root:universal"))

	return idx
}

// newWorkspace returns a ready Workspace.
// parentClusterID: logical cluster the Workspace object lives in.
// clusterID: logical cluster the Workspace is scheduled to.
func newWorkspace(name, parentClusterID, clusterID string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{logicalcluster.AnnotationKey: parentClusterID}},
		Spec:       tenancyv1alpha1.WorkspaceSpec{Cluster: clusterID},
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	}
}

func newLogicalCluster(clusterID, workspaceType string) *corev1alpha1.LogicalCluster {
	annotations := map[string]string{logicalcluster.AnnotationKey: clusterID}
	if workspaceType != "" {
		annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey] = workspaceType
	}
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.LogicalClusterName, Annotations: annotations},
	}
}

type resolved struct {
	called        bool
	url           string
	cluster       *request.Cluster
	workspaceType logicalcluster.Path
	shard         string
	mount         *Mount
}

func capture(got *resolved) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		*got = resolved{
			called:        true,
			url:           req.URL.String(),
			cluster:       request.ClusterFrom(ctx),
			workspaceType: WorkspaceTypeFrom(ctx),
			shard:         ShardNameFrom(ctx),
			mount:         MountFrom(ctx),
		}
	})
}

type resolveCase struct {
	url string

	wantStatus        int
	wantCluster       *request.Cluster
	wantWorkspaceType logicalcluster.Path
	wantShard         string
	wantMount         *Mount
}

// resolveCases is shared by both filter variants.
// URLs all carry a /clusters/<segment> prefix.
var resolveCases = map[string]resolveCase{
	"wildcard": {
		url:         "/clusters/*/apis",
		wantCluster: &request.Cluster{Wildcard: true},
	},
	"workspace path": {
		url:               "/clusters/" + wsWorkspacePath + "/apis/v1?watch=true",
		wantCluster:       &request.Cluster{Name: wsClusterID},
		wantWorkspaceType: logicalcluster.NewPath("root:universal"),
		wantShard:         betaShard,
	},
	"workspace path without trailing path": {
		url:               "/clusters/" + orgWorkspacePath,
		wantCluster:       &request.Cluster{Name: orgClusterID},
		wantWorkspaceType: logicalcluster.NewPath("root:organization"),
		wantShard:         rootShard,
	},
	"cluster ID": {
		url:               "/clusters/" + wsClusterID + "/apis",
		wantCluster:       &request.Cluster{Name: wsClusterID},
		wantWorkspaceType: logicalcluster.NewPath("root:universal"),
		wantShard:         betaShard,
	},
	"unknown cluster ID": {
		url:        "/clusters/system:unknown/apis",
		wantStatus: http.StatusForbidden,
	},
	"unknown workspace path": {
		url:        "/clusters/root:nope/apis",
		wantStatus: http.StatusForbidden,
	},
	"invalid segment": {
		url:        "/clusters/root:Bad!/apis",
		wantStatus: http.StatusBadRequest,
	},
	"error code": {
		url:        "/clusters/" + downWorkspacePath + "/apis",
		wantStatus: http.StatusServiceUnavailable,
	},
	"mount by workspace path": {
		url:       "/clusters/" + mntWorkspacePath + "/apis",
		wantShard: rootShard,
		wantMount: &Mount{
			Workspace:     logicalcluster.NewPath(mntWorkspacePath),
			ParentCluster: orgClusterID,
		},
	},
	"mount by parent cluster ID": {
		url:       "/clusters/" + orgClusterID + ":mnt/apis",
		wantShard: rootShard,
		wantMount: &Mount{
			Workspace:     logicalcluster.NewPath(orgClusterID + ":mnt"),
			ParentCluster: orgClusterID,
		},
	},
}

func assertResolved(t *testing.T, tc resolveCase, rec *httptest.ResponseRecorder, got resolved, wantURL string) {
	t.Helper()

	if tc.wantStatus != 0 {
		require.False(t, got.called, "handler must not be called")
		require.Equal(t, tc.wantStatus, rec.Code)
		return
	}

	require.True(t, got.called, "handler must be called")
	require.Equal(t, wantURL, got.url)
	require.Equal(t, tc.wantCluster, got.cluster)
	require.Equal(t, tc.wantWorkspaceType, got.workspaceType)
	require.Equal(t, tc.wantShard, got.shard)

	if tc.wantMount == nil {
		require.Nil(t, got.mount)
		return
	}
	require.NotNil(t, got.mount)
	require.Equal(t, mntURL, got.mount.URL.String())
	require.Equal(t, tc.wantMount.Workspace, got.mount.Workspace)
	require.Equal(t, tc.wantMount.ParentCluster, got.mount.ParentCluster)
}

func TestWithClusterResolution(t *testing.T) {
	t.Parallel()

	// stripped is the URL after the /clusters/<segment> prefix was removed.
	stripped := map[string]string{
		"wildcard":                             "/apis",
		"workspace path":                       "/apis/v1?watch=true",
		"workspace path without trailing path": "/",
		"cluster ID":                           "/apis",
		"mount by workspace path":              "/apis",
		"mount by parent cluster ID":           "/apis",
	}

	idx := newResolveIndex()

	for name, tc := range resolveCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got resolved
			handler := WithClusterResolution(capture(&got), idx)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, http.NoBody))

			assertResolved(t, tc, rec, got, stripped[name])
		})
	}

	t.Run("no clusters prefix", func(t *testing.T) {
		t.Parallel()

		var got resolved
		handler := WithClusterResolution(capture(&got), idx)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

		require.True(t, got.called)
		require.Equal(t, "/healthz", got.url)
		require.Nil(t, got.cluster)
	})

	t.Run("empty segment", func(t *testing.T) {
		t.Parallel()

		var got resolved
		handler := WithClusterResolution(capture(&got), idx)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters//apis", http.NoBody))

		require.False(t, got.called)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestWithClusterResolutionPattern(t *testing.T) {
	t.Parallel()

	idx := newResolveIndex()

	for name, tc := range resolveCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var got resolved
			handler := WithClusterResolutionPattern(capture(&got), idx, "cluster")

			mux := http.NewServeMux()
			mux.Handle("/clusters/{cluster}", handler)
			mux.Handle("/clusters/{cluster}/{trail...}", handler)

			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, http.NoBody))

			assertResolved(t, tc, rec, got, tc.url)
		})
	}

	t.Run("no matched segment", func(t *testing.T) {
		t.Parallel()

		var got resolved
		handler := WithClusterResolutionPattern(capture(&got), idx, "cluster")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

		require.True(t, got.called)
		require.Equal(t, "/healthz", got.url)
		require.Nil(t, got.cluster)
	})
}
