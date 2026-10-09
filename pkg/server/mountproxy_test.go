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

package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	auditinternal "k8s.io/apiserver/pkg/apis/audit"
	"k8s.io/apiserver/pkg/audit"
	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/mounts"
	"github.com/kcp-dev/kcp/pkg/server/filters"
)

const (
	testUserHeader    = "X-Remote-User"
	testUIDHeader     = "X-Remote-Uid"
	testGroupHeader   = "X-Remote-Group"
	testWarrantHeader = "X-Remote-Extra-Authorization.kcp.io%2fwarrant"
	testScopesHeader  = "X-Remote-Extra-Authentication.kcp.io%2fscopes"
)

// newMountIndex returns a local index for shard "test-shard" holding the root
// logical cluster and a mounted workspace root:mnt pointing at mountURL.
func newMountIndex(mountURL string) *index.State {
	idx := index.New(nil)
	idx.UpsertLogicalCluster("test-shard", &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.LogicalClusterName, Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
	})
	idx.UpsertWorkspace("test-shard", &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "mnt", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			URL:   mountURL,
			Mount: &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{Name: "ref"}},
		},
		Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
	})
	return idx
}

// withUser stands in for the authentication filter: it puts u (if non-nil)
// into the request context and leaves the request untouched otherwise.
func withUser(u userinfo.Info, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if u != nil {
			ctx = request.WithUser(ctx, u)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// allowAll is an authorizer that permits everything, for tests that are not
// about the access decision itself.
var allowAll = authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionAllow, "", nil
})

func TestWithLocalProxy_MountIsResolvedNotForwarded(t *testing.T) {
	t.Parallel()

	var (
		gotTarget  *filters.MountTarget
		gotCluster *request.Cluster
		gotPath    string
		called     bool
	)
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		gotTarget = filters.MountTargetFrom(r.Context())
		gotCluster = request.ClusterFrom(r.Context())
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	h, err := WithLocalProxy(downstream, "test-shard", "", newMountIndex("https://mount.example.com/base"))
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.True(t, called, "a mount request must continue into the handler chain")
	require.NotNil(t, gotTarget, "the mount target must be on the context")
	require.Equal(t, "https://mount.example.com/base", gotTarget.URL.String())
	require.Equal(t, logicalcluster.NewPath("root:mnt"), gotTarget.Workspace)
	require.Equal(t, logicalcluster.Name("root"), gotTarget.ParentCluster)
	require.Nil(t, gotCluster, "a mount has no logical cluster")
	require.Equal(t, "/api/v1/secrets", gotPath, "the workspace prefix must be stripped")
}

func TestWithLocalProxy_MountRejectsImpersonation(t *testing.T) {
	t.Parallel()

	for header, value := range map[string]string{
		"Impersonate-User":            "admin",
		"Impersonate-Group":           "system:masters",
		"Impersonate-Uid":             "1234",
		"Impersonate-Extra-Scopes":    "cluster:root",
		"Impersonate-Extra-Something": "x",
	} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()
			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("an impersonating request for a mount must not continue into the handler chain")
			})
			h, err := WithLocalProxy(downstream, "test-shard", "", newMountIndex("https://mount.example.com"))
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
			req.Header.Set(header, value)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusForbidden, rr.Code, "body=%s", rr.Body.String())
		})
	}
}

func TestWithLocalProxy_MountWithInsecureURLIsNotRouted(t *testing.T) {
	t.Parallel()

	for _, mountURL := range []string{
		"http://mount.example.com",
		"https://user:pass@mount.example.com",
		"https://mount.example.com/?watch=true",
	} {
		t.Run(mountURL, func(t *testing.T) {
			t.Parallel()
			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("a request for a mount with an unacceptable target must not continue into the handler chain")
			})
			h, err := WithLocalProxy(downstream, "test-shard", "", newMountIndex(mountURL))
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusNotFound, rr.Code, "body=%s", rr.Body.String())
		})
	}
}

type forwardedRequest struct {
	header http.Header
	host   string
	path   string
}

type fakeAuditSink struct{}

func (fakeAuditSink) ProcessEvents(...*auditinternal.Event) bool { return true }

// TestWithMountProxy_ForwardsAuthenticatedIdentityOnly exercises the chain a
// mount request takes on a shard: WithLocalProxy resolves it, authentication
// puts the user on the context, WithMountProxy forwards it. The target must
// see the authenticated identity in the identity headers and nothing the
// caller supplied: no credentials, no impersonation, no forged identity.
func TestWithMountProxy_ForwardsAuthenticatedIdentityOnly(t *testing.T) {
	t.Parallel()

	forgedHeaders := http.Header{
		testUserHeader:         {"admin"},
		testUIDHeader:          {"admin-uid"},
		testGroupHeader:        {"system:masters", "system:kcp:external-logical-cluster-admin"},
		testWarrantHeader:      {`{"user":"attacker","groups":["system:masters"]}`},
		"Authorization":        {"Bearer some-token"},
		"X-Forwarded-For":      {"10.0.0.1"},
		"Impersonate-User":     {"admin"},
		"Impersonate-Group":    {"system:masters"},
		"Impersonate-Extra-Id": {"x"},
	}

	tests := []struct {
		name       string
		user       userinfo.Info
		wantUser   []string
		wantGroups []string
		wantScopes []string
	}{
		{
			name: "anonymous user is forwarded without identity",
			user: &userinfo.DefaultInfo{Name: userinfo.Anonymous, Groups: []string{userinfo.AllUnauthenticated}},
		},
		{
			name: "authenticated user replaces forged identity",
			user: &userinfo.DefaultInfo{
				Name:   "alice",
				Groups: []string{"team:a", userinfo.AllAuthenticated},
				Extra:  map[string][]string{"authentication.kcp.io/scopes": {"cluster:root"}},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"team:a", userinfo.AllAuthenticated},
			wantScopes: []string{"cluster:root"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forwarded := make(chan forwardedRequest, 1)
			backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded <- forwardedRequest{header: r.Header.Clone(), host: r.Host, path: r.URL.Path}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(backend.Close)

			var (
				gotAttrs   authorizer.Attributes
				gotCluster *request.Cluster
			)
			authz := authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				gotAttrs = a
				gotCluster = request.ClusterFrom(ctx)
				return authorizer.DecisionAllow, "", nil
			})

			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("request for a mounted workspace must not reach the shard API handler")
			})
			// Impersonation is rejected by WithLocalProxy before authentication; inject
			// the headers after it to prove the mount proxy drops them regardless.
			injectForged := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					for k, vs := range forgedHeaders {
						r.Header[k] = append([]string(nil), vs...)
					}
					next.ServeHTTP(w, r)
				})
			}
			h := WithMountProxy(downstream, backend.Client().Transport, authz)
			h = injectForged(withUser(tc.user, h))
			h, err := WithLocalProxy(h, "test-shard", "", newMountIndex(backend.URL+"/base"))
			require.NoError(t, err)

			ctx := audit.WithAuditContext(t.Context())
			require.NoError(t, audit.AuditContextFrom(ctx).Init(audit.RequestAuditConfig{Level: auditinternal.LevelMetadata}, fakeAuditSink{}))
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())

			got := <-forwarded
			require.Equal(t, "/base/api/v1/secrets", got.path, "request path must be appended to the mount target path")
			require.Equal(t, backend.Listener.Addr().String(), got.host, "Host must be rewritten to the mount target")
			require.Empty(t, got.header.Values("Authorization"), "the caller's credentials must never reach the mount target")
			require.Empty(t, got.header.Values("X-Forwarded-For"))
			for k := range got.header {
				require.NotContains(t, k, "Impersonate-", "impersonation headers must not reach the mount target")
			}
			require.Equal(t, tc.wantUser, got.header.Values(testUserHeader), "user header")
			require.Equal(t, tc.wantGroups, got.header.Values(testGroupHeader), "group header")
			require.Equal(t, tc.wantScopes, got.header.Values(testScopesHeader), "extra header")
			require.Empty(t, got.header.Values(testWarrantHeader), "forged warrant must not be forwarded")
			require.Empty(t, got.header.Values(testUIDHeader), "forged uid must not be forwarded")

			require.NotNil(t, gotAttrs, "access to the mount must be authorized")
			require.Equal(t, "get", gotAttrs.GetVerb())
			require.Equal(t, "workspaces", gotAttrs.GetResource())
			require.Equal(t, tenancyv1alpha1.SchemeGroupVersion.Group, gotAttrs.GetAPIGroup())
			require.Equal(t, "mnt", gotAttrs.GetName())
			require.Equal(t, tc.user.GetName(), gotAttrs.GetUser().GetName())
			require.NotNil(t, gotCluster)
			require.Equal(t, logicalcluster.Name("root"), gotCluster.Name, "authorization must run against the parent of the mount")

			ac := audit.AuditContextFrom(ctx)
			target, ok := ac.GetEventAnnotation(mountTargetAuditAnnotation)
			require.True(t, ok, "audit event must record the mount target")
			require.Equal(t, backend.URL, target)
			ws, ok := ac.GetEventAnnotation(mountWorkspaceAuditAnnotation)
			require.True(t, ok, "audit event must record the mounted workspace")
			require.Equal(t, "root:mnt", ws)
		})
	}
}

func TestWithMountProxy_Errors(t *testing.T) {
	t.Parallel()

	alice := &userinfo.DefaultInfo{Name: "alice", Groups: []string{userinfo.AllAuthenticated}}
	allow := allowAll

	tests := []struct {
		name       string
		user       userinfo.Info
		authz      authorizer.Authorizer
		closeFirst bool
		wantStatus int
	}{
		{
			name:       "no user in context",
			user:       nil,
			authz:      allow,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "access denied",
			user: alice,
			authz: authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionDeny, "nope", nil
			}),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "no opinion is denied",
			user: alice,
			authz: authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionNoOpinion, "", nil
			}),
			wantStatus: http.StatusForbidden,
		},
		{
			name: "authorizer error",
			user: alice,
			authz: authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionNoOpinion, "", errors.New("boom")
			}),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "mount target unreachable",
			user:       alice,
			authz:      allow,
			closeFirst: true,
			wantStatus: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("the request must not reach the mount target")
			}))
			transport := backend.Client().Transport
			if tc.closeFirst {
				backend.Close()
			} else {
				t.Cleanup(backend.Close)
			}

			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("request for a mounted workspace must not reach the shard API handler")
			})
			h := withUser(tc.user, WithMountProxy(downstream, transport, tc.authz))
			h, err := WithLocalProxy(h, "test-shard", "", newMountIndex(backend.URL))
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, tc.wantStatus, rr.Code, "body=%s", rr.Body.String())
		})
	}
}

// TestWithMountProxy_ClientDisconnectIsNotAnError checks that a client going
// away mid-request is not reported as the mount target failing. A cancelled watch
// is ordinary client behaviour, so it must not be logged as an error, and there
// is nobody left to send a 502 to.
func TestWithMountProxy_ClientDisconnectIsNotAnError(t *testing.T) {
	t.Parallel()

	alice := &userinfo.DefaultInfo{Name: "alice", Groups: []string{userinfo.AllAuthenticated}}

	// A backend that never answers, so the round trip ends only when the request
	// context is cancelled, which is what a client hanging up looks like.
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(backend.Close)

	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request for a mounted workspace must not reach the shard API handler")
	})
	h := withUser(alice, WithMountProxy(downstream, backend.Client().Transport, allowAll))
	h, err := WithLocalProxy(h, "test-shard", "", newMountIndex(backend.URL))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rr, req)
	}()
	cancel()
	<-done

	// Nothing was written: no 502 for a client that is no longer there.
	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Empty(t, rr.Body.String())
}

func TestWithMountProxy_PassesNonMountRequestsThrough(t *testing.T) {
	t.Parallel()

	called := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})
	h := WithMountProxy(downstream, http.DefaultTransport, allowAll)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/secrets", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	require.True(t, called)
	require.Equal(t, http.StatusTeapot, rr.Code)
}

// TestWithMountProxy_RequiresAuthorizer makes sure a missing authorizer is a
// programming error rather than a silent allow-everything path: entering a
// mounted workspace is an access decision.
func TestWithMountProxy_RequiresAuthorizer(t *testing.T) {
	t.Parallel()

	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	require.PanicsWithValue(t, "an authorizer is required for the mount proxy", func() {
		WithMountProxy(downstream, http.DefaultTransport, nil)
	})
	require.PanicsWithValue(t, "a transport is required for the mount proxy", func() {
		WithMountProxy(downstream, nil, allowAll)
	})
}

// TestWithMountProxy_CountsMountHops verifies the loop guard. A mount target may
// be kcp itself, so a mount can be pointed at a path that resolves back to a
// mount. Each hop must be counted, and a request that has already taken the
// maximum number of hops must be refused instead of forwarded again.
func TestWithMountProxy_CountsMountHops(t *testing.T) {
	t.Parallel()

	alice := &userinfo.DefaultInfo{Name: "alice", Groups: []string{userinfo.AllAuthenticated}}

	tests := []struct {
		name       string
		inbound    []string // values of the hop header on the incoming request
		wantStatus int
		wantHops   string // value forwarded to the target, empty when not forwarded
	}{
		{
			name:       "a fresh request is the first hop",
			wantStatus: http.StatusOK,
			wantHops:   "1",
		},
		{
			name:       "a request that already took a hop is counted on",
			inbound:    []string{"1"},
			wantStatus: http.StatusOK,
			wantHops:   "2",
		},
		{
			name:       "a request at the limit is refused as a loop",
			inbound:    []string{strconv.Itoa(mounts.MaxHops)},
			wantStatus: http.StatusLoopDetected,
		},
		{
			name:       "a request past the limit is refused as a loop",
			inbound:    []string{strconv.Itoa(mounts.MaxHops + 7)},
			wantStatus: http.StatusLoopDetected,
		},
		{
			// A client cannot hide hops: the proxy replaces the header, so a
			// forged value only ever applies to that client's own request.
			name:       "a forged value is replaced, not appended",
			inbound:    []string{"0", "2"},
			wantStatus: http.StatusOK,
			wantHops:   "1",
		},
		{
			name:       "an unparsable value counts as no hops",
			inbound:    []string{"not-a-number"},
			wantStatus: http.StatusOK,
			wantHops:   "1",
		},
		{
			name:       "a negative value counts as no hops",
			inbound:    []string{"-5"},
			wantStatus: http.StatusOK,
			wantHops:   "1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			forwarded := make(chan http.Header, 1)
			backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded <- r.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(backend.Close)

			downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("request for a mounted workspace must not reach the shard API handler")
			})
			h := withUser(alice, WithMountProxy(downstream, backend.Client().Transport, allowAll))
			h, err := WithLocalProxy(h, "test-shard", "", newMountIndex(backend.URL))
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/root:mnt/api/v1/secrets", http.NoBody)
			for _, v := range tc.inbound {
				req.Header.Add(mounts.HopsHeader, v)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			require.Equal(t, tc.wantStatus, rr.Code, "body=%s", rr.Body.String())

			if tc.wantHops == "" {
				require.Empty(t, forwarded, "a looping request must not be forwarded")
				return
			}
			got := <-forwarded
			require.Equal(t, []string{tc.wantHops}, got.Values(mounts.HopsHeader), "hop count forwarded to the mount target")
		})
	}
}
