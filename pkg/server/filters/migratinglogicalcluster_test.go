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

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestWithBlockMigratingLogicalClusters(t *testing.T) {
	t.Parallel()

	const migrating = logicalcluster.Name("migrating-lc")

	isMigrating := func(name logicalcluster.Name) bool {
		return name == migrating
	}

	testCases := []struct {
		name string

		cluster     *request.Cluster
		requestInfo *request.RequestInfo
		user        user.Info
		query       string

		wantDelegated  bool
		wantStatusCode int
		wantRetryAfter bool
	}{
		{
			name:          "no cluster passes through",
			cluster:       nil,
			wantDelegated: true,
		},
		{
			name:          "non-migrating cluster passes through",
			cluster:       &request.Cluster{Name: "other-lc"},
			wantDelegated: true,
		},
		{
			name:          "external logical cluster admin passes through",
			cluster:       &request.Cluster{Name: migrating},
			user:          &user.DefaultInfo{Groups: []string{bootstrappolicy.SystemExternalLogicalClusterAdmin}},
			requestInfo:   &request.RequestInfo{IsResourceRequest: true, Verb: "list"},
			query:         "resourceVersion=1234",
			wantDelegated: true,
		},
		{
			name:           "migrating cluster without resourceVersion is blocked with Retry-After",
			cluster:        &request.Cluster{Name: migrating},
			requestInfo:    &request.RequestInfo{IsResourceRequest: true, Verb: "list"},
			wantStatusCode: http.StatusServiceUnavailable,
			wantRetryAfter: true,
		},
		{
			name:           "migrating cluster with resourceVersion=0 is blocked with Retry-After",
			cluster:        &request.Cluster{Name: migrating},
			requestInfo:    &request.RequestInfo{IsResourceRequest: true, Verb: "list"},
			query:          "resourceVersion=0",
			wantStatusCode: http.StatusServiceUnavailable,
			wantRetryAfter: true,
		},
		{
			name:           "migrating list with resourceVersion is expired",
			cluster:        &request.Cluster{Name: migrating},
			requestInfo:    &request.RequestInfo{IsResourceRequest: true, Verb: "list"},
			query:          "resourceVersion=1234&resourceVersionMatch=NotOlderThan",
			wantStatusCode: http.StatusGone,
			wantRetryAfter: false,
		},
		{
			name:           "migrating watch with resourceVersion is expired",
			cluster:        &request.Cluster{Name: migrating},
			requestInfo:    &request.RequestInfo{IsResourceRequest: true, Verb: "watch"},
			query:          "resourceVersion=1234&watch=true",
			wantStatusCode: http.StatusGone,
			wantRetryAfter: false,
		},
		{
			name:           "migrating get with resourceVersion is blocked with Retry-After, not expired",
			cluster:        &request.Cluster{Name: migrating},
			requestInfo:    &request.RequestInfo{IsResourceRequest: true, Verb: "get"},
			query:          "resourceVersion=1234",
			wantStatusCode: http.StatusServiceUnavailable,
			wantRetryAfter: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			delegated := false
			handler := WithBlockMigratingLogicalClusters(
				http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					delegated = true
					w.WriteHeader(http.StatusOK)
				}),
				isMigrating,
			)

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/apis/foo/v1/bar?"+tc.query, http.NoBody)
			ctx := req.Context()
			if tc.cluster != nil {
				ctx = request.WithCluster(ctx, *tc.cluster)
			}
			if tc.requestInfo != nil {
				ctx = request.WithRequestInfo(ctx, tc.requestInfo)
			}
			if tc.user != nil {
				ctx = request.WithUser(ctx, tc.user)
			}
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if tc.wantDelegated {
				if !delegated {
					t.Fatalf("expected request to be delegated to the next handler")
				}
				return
			}

			if delegated {
				t.Fatalf("expected request to be intercepted, but it was delegated")
			}
			if rec.Code != tc.wantStatusCode {
				t.Errorf("expected status code %d, got %d", tc.wantStatusCode, rec.Code)
			}
			if got := rec.Header().Get("Retry-After") != ""; got != tc.wantRetryAfter {
				t.Errorf("expected Retry-After present=%v, got header %q", tc.wantRetryAfter, rec.Header().Get("Retry-After"))
			}
		})
	}
}
