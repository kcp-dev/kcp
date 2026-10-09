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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authentication"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/mounts"
	"github.com/kcp-dev/kcp/pkg/proxy/authheaders"
	"github.com/kcp-dev/kcp/pkg/server/filters"
)

const (
	// mountTargetAuditAnnotation records the scheme and host of the mount
	// target a request was forwarded to.
	mountTargetAuditAnnotation = "mount.tenancy.kcp.io/target"
	// mountWorkspaceAuditAnnotation records the path of the mounted workspace
	// a request was forwarded for.
	mountWorkspaceAuditAnnotation = "mount.tenancy.kcp.io/workspace"
	// mountLoopAuditAnnotation records the hop count of a request that was
	// refused because the mount targets form a loop.
	mountLoopAuditAnnotation = "mount.tenancy.kcp.io/loop-detected-at-hop"
)

// newMountProxyTransport returns the transport the local proxy uses to connect
// to mount targets. The server certificate of the target is always verified,
// against the system roots and, if given, the additional CA bundle in caFile.
// If a client certificate is given, it is presented to the target; kcp
// deployments sign it with the front-proxy requestheader CA so that mounts
// pointing back into kcp can trust the forwarded identity headers.
func newMountProxyTransport(clientCertFile, clientKeyFile, caFile string) (http.RoundTripper, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if caFile != "" {
		caCert, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read mount proxy CA file %q: %w", caFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("mount proxy CA file %q contains no certificates", caFile)
		}
		tlsConfig.RootCAs = pool
	}

	if clientCertFile != "" || clientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load mount proxy client certificate %q or key %q: %w", clientCertFile, clientKeyFile, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return transport, nil
}

// WithMountProxy forwards requests for mounted workspaces to their mount
// target. The local proxy resolves the target in front of the handler chain
// and stores it in the request context; this filter sits after
// authentication, audit, flow control and impersonation and before
// authorization, mirroring the virtual workspace proxy.
//
// The caller's credentials never reach the target: the Authorization header
// and any impersonation headers are dropped and the identity the shard
// authenticated is forwarded via the request-header identity headers instead.
// Before forwarding, the caller must be allowed to get the Workspace object
// describing the mount in its parent logical cluster.
func WithMountProxy(apiHandler http.Handler, transport http.RoundTripper, authz authorizer.Authorizer) http.Handler {
	if transport == nil {
		panic("a transport is required for the mount proxy")
	}
	// Entering a mounted workspace is an access decision, so a missing authorizer
	// must not degrade into forwarding everything.
	if authz == nil {
		panic("an authorizer is required for the mount proxy")
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		target := filters.MountTargetFrom(req.Context())
		if target == nil {
			apiHandler.ServeHTTP(w, req)
			return
		}

		ctx := req.Context()
		targetOrigin := target.URL.Scheme + "://" + target.URL.Host
		logger := klog.FromContext(ctx).WithValues("workspace", target.Workspace.String(), "mountTarget", targetOrigin)

		audit.AddAuditAnnotation(ctx, mountWorkspaceAuditAnnotation, target.Workspace.String())
		audit.AddAuditAnnotation(ctx, mountTargetAuditAnnotation, targetOrigin)

		u, ok := request.UserFrom(ctx)
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("no user in context")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		hops := mounts.HopsFrom(req.Header)
		if hops >= mounts.MaxHops {
			logger.WithValues("hops", hops).Info("refusing to forward request to mount target: too many mount hops, the mount targets likely form a loop")
			audit.AddAuditAnnotation(ctx, mountLoopAuditAnnotation, strconv.Itoa(hops))
			responsewriters.ErrorNegotiated(
				&apierrors.StatusError{ErrStatus: metav1.Status{
					Status: metav1.StatusFailure,
					// 508 Loop Detected. No metav1 reason describes this, and an
					// unrelated one would mislead clients, so only the code is set.
					Code:    http.StatusLoopDetected,
					Message: fmt.Sprintf("request for workspace %q was forwarded through %d mount targets; the mount targets form a loop", target.Workspace.String(), hops),
				}},
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		_, name := target.Workspace.Split()
		attrs := authorizer.AttributesRecord{
			User:            u,
			Verb:            "get",
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
			Resource:        "workspaces",
			Name:            name,
			ResourceRequest: true,
			Path:            "/apis/" + tenancyv1alpha1.SchemeGroupVersion.String() + "/workspaces/" + name,
		}
		authzCtx := request.WithCluster(ctx, request.Cluster{Name: target.ParentCluster})
		decision, reason, err := authz.Authorize(authzCtx, attrs)
		if err != nil {
			logger.Error(err, "failed to authorize access to mounted workspace")
			responsewriters.InternalError(w, req, err)
			return
		}
		if decision != authorizer.DecisionAllow {
			logger.V(4).WithValues("user", u.GetName(), "reason", reason).Info("access to mounted workspace denied")
			responsewriters.Forbidden(attrs, w, req, reason, errorCodecs)
			return
		}

		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target.URL)
				setMountProxyHeaders(pr.Out.Header, u, hops+1)
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				if logging.IsClientDisconnect(r.Context(), err) {
					// Routine for a long-running request such as a watch: there is
					// no client left to answer, and this is not a fault of ours or
					// of the mount target.
					logger.V(4).Info("client went away while forwarding request to mount target", "err", err)
					return
				}
				logger.Error(err, "failed to forward request to mount target")
				responsewriters.ErrorNegotiated(
					&apierrors.StatusError{ErrStatus: metav1.Status{
						Status:  metav1.StatusFailure,
						Code:    http.StatusBadGateway,
						Reason:  metav1.StatusReasonServiceUnavailable,
						Message: fmt.Sprintf("the mount target of workspace %q is unavailable", target.Workspace.String()),
					}},
					errorCodecs, schema.GroupVersion{}, w, r,
				)
			},
		}

		logger.V(4).Info("forwarding request to mount target")
		proxy.ServeHTTP(w, req)
	})
}

// setMountProxyHeaders prepares the headers of a request forwarded to a mount
// target: the caller's own credentials and impersonation headers are removed,
// and the identity the shard authenticated replaces any inbound identity
// headers. Anonymous callers are forwarded without identity headers.
func setMountProxyHeaders(header http.Header, u user.Info, hops int) {
	// Set, not Add: this replaces any value the client sent, so only a count this
	// shard or another kcp shard wrote is ever forwarded.
	header.Set(mounts.HopsHeader, strconv.Itoa(hops))

	header.Del("Authorization")
	authentication.ScrubImpersonationHeaders(header)

	if u == nil || u.GetName() == user.Anonymous {
		authheaders.ClearAuthHeaders(header, authheaders.DefaultUserHeader, authheaders.DefaultGroupHeader, authheaders.DefaultExtraHeaderPrefix)
		return
	}
	authheaders.SetAuthHeaders(header, u, authheaders.DefaultUserHeader, authheaders.DefaultGroupHeader, authheaders.DefaultExtraHeaderPrefix)
}
