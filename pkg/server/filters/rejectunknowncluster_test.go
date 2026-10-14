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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

func TestWithRejectUnknownCluster(t *testing.T) {
	t.Parallel()

	const clusterID = "25t3xbr5iceb0155"
	alice := &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}}

	tests := map[string]struct {
		ctx        func(context.Context) context.Context
		wantStatus int
	}{
		"no cluster": {
			ctx:        func(ctx context.Context) context.Context { return request.WithUser(ctx, alice) },
			wantStatus: http.StatusOK,
		},
		"wildcard": {
			ctx: func(ctx context.Context) context.Context {
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Wildcard: true}), alice)
			},
			wantStatus: http.StatusOK,
		},
		"resolved cluster": {
			ctx: func(ctx context.Context) context.Context {
				ctx = request.WithCluster(ctx, request.Cluster{Name: clusterID})
				ctx = lookup.WithShardName(ctx, "test-shard")
				return request.WithUser(ctx, alice)
			},
			wantStatus: http.StatusOK,
		},
		"unknown cluster, regular user": {
			ctx: func(ctx context.Context) context.Context {
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Name: clusterID}), alice)
			},
			wantStatus: http.StatusForbidden,
		},
		"unknown cluster, system:masters": {
			ctx: func(ctx context.Context) context.Context {
				u := &userinfo.DefaultInfo{Name: "admin", Groups: []string{bootstrap.SystemMastersGroup}}
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Name: clusterID}), u)
			},
			wantStatus: http.StatusOK,
		},
		"unknown cluster, logical-cluster-admin": {
			ctx: func(ctx context.Context) context.Context {
				u := &userinfo.DefaultInfo{Name: "scheduler", Groups: []string{bootstrap.SystemLogicalClusterAdmin}}
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Name: clusterID}), u)
			},
			wantStatus: http.StatusOK,
		},
		"unknown cluster, external-logical-cluster-admin": {
			ctx: func(ctx context.Context) context.Context {
				u := &userinfo.DefaultInfo{Name: "external", Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}}
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Name: clusterID}), u)
			},
			wantStatus: http.StatusOK,
		},
		"unknown cluster, kcp-admin": {
			ctx: func(ctx context.Context) context.Context {
				u := &userinfo.DefaultInfo{Name: "kcp-admin", Groups: []string{bootstrap.SystemKcpAdminGroup}}
				return request.WithUser(request.WithCluster(ctx, request.Cluster{Name: clusterID}), u)
			},
			wantStatus: http.StatusOK,
		},
		"unknown cluster, no user": {
			ctx: func(ctx context.Context) context.Context {
				return request.WithCluster(ctx, request.Cluster{Name: clusterID})
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			called := false
			handler := WithRejectUnknownCluster(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequestWithContext(tc.ctx(t.Context()), http.MethodGet, "/api/v1/configmaps", http.NoBody)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			require.Equal(t, tc.wantStatus, rr.Code, "body=%s", rr.Body.String())
			require.Equal(t, tc.wantStatus == http.StatusOK, called)
		})
	}
}
