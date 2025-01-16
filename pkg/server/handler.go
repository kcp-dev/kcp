/*
Copyright 2021 The KCP Authors.

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
	"fmt"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"sort"
	"strings"

	"github.com/emicklei/go-restful/v3"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/kcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	apiserverdiscovery "k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	clientgotransport "k8s.io/client-go/transport"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controlplane/apiserver/miniaggregator"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

const (
	passthroughHeader = "X-Kcp-Api-V1-Discovery-Passthrough"
)

// WithVirtualWorkspacesProxy proxies internal requests to virtual workspaces (i.e., requests that did
// not go through the front proxy) to the external virtual workspaces server. Proxying is required to avoid
// certificate verification errors because these requests typically come from the kcp loopback client, and it is
// impossible to use that client against any server other than kcp.
func WithVirtualWorkspacesProxy(apiHandler http.Handler, shardVirtualWorkspaceURL *url.URL, transport http.RoundTripper, proxy *httputil.ReverseProxy) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		logger := klog.FromContext(req.Context())

		if !strings.HasPrefix(req.URL.Path, "/services/") {
			apiHandler.ServeHTTP(w, req)
			return
		}

		if shardVirtualWorkspaceURL == nil || transport == nil {
			// This handler func is only installed when these are both set. If this happens, it means we've regressed
			// in the installation of this handler func, and a panic is appropriate.
			panic("both shardVirtualWorkspaceURL and transport are required")
		}

		user, ok := request.UserFrom(req.Context())
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("no user in context")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		proxy.Transport = clientgotransport.NewImpersonatingRoundTripper(
			clientgotransport.ImpersonationConfig{
				UserName: user.GetName(),
				UID:      user.GetUID(),
				Groups:   user.GetGroups(),
				Extra:    user.GetExtra(),
			},
			transport,
		)

		logger.V(4).Info("proxying virtual workspace", "target", req.URL.String())
		proxy.ServeHTTP(w, req)
	}
}

func mergeCRDsIntoCoreGroup(crdLister kcp.ClusterAwareCRDClusterLister, crdHandler, coreHandler func(res http.ResponseWriter, req *http.Request)) restful.FilterFunction {
	return func(req *restful.Request, res *restful.Response, chain *restful.FilterChain) {
		ctx := req.Request.Context()
		requestInfo, ok := request.RequestInfoFrom(ctx)
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
				// TODO is this the right Codecs?
				errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, res.ResponseWriter, req.Request,
			)
			return
		}

		// If it's not the core group, pass through
		if requestInfo.APIGroup != "" {
			chain.ProcessFilter(req, res)
			return
		}

		//
		// from here on we know it's the core group
		//

		if !requestInfo.IsResourceRequest && (req.Request.URL.Path == "/api/v1" || req.Request.URL.Path == "/api/v1/") {
			// This is a discovery request. We may need to combine discovery from the GenericControlPlane (which has built-in v1 types)
			// and CRDs, if there are any v1 CRDs.

			// Because of the way the http handlers are configured for /api/v1, we have to do something a bit unique to make this work.
			// /api/v1 is ultimately served by the GoRestfulContainer. This means we have to put a filter on it to be able to change the
			// behavior. And because the filter runs for the client's initial /api/v1 request, and when we pass the request down to
			// the generic control plane to get its discovery for /api/v1, we have to do something to short circuit our filter to
			// avoid infinite recursion. This is done below using passthroughHeader.
			//
			// The initial request, from a client, won't have this header set. We set it and send the /api/v1 request to the generic control
			// plane. This re-invokes this filter, but because the header is set, we pass the request through to the rest of the filter chain,
			// meaning it will be sent to the generic control plane to return its /api/v1 discovery.

			// If we are retrieving the GenericControlPlane's v1 APIResources, pass it through to the filter chain.
			if _, passthrough := req.Request.Header[passthroughHeader]; passthrough {
				chain.ProcessFilter(req, res)
				return
			}

			// If we're here, it means it's an initial /api/v1 request from a client.

			serveCoreV1Discovery(ctx, crdLister, coreHandler, res.ResponseWriter, req.Request)
			return
		}

		if requestInfo.IsResourceRequest {
			// This is a CRUD request for something like pods. Try to see if there is a CRD for the resource. If so, let the CRD
			// server handle it.
			crdName := requestInfo.Resource + ".core"

			clusterName, wildcard, err := request.ClusterNameOrWildcardFrom(req.Request.Context())
			if err != nil {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(fmt.Errorf("no cluster found in the context")),
					// TODO is this the right Codecs?
					errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, res.ResponseWriter, req.Request,
				)
				return
			}
			if wildcard {
				// this is the only case where wildcard works for a list because this is our special CRD lister that handles it.
				clusterName = "*"
			}

			if _, err := crdLister.Cluster(clusterName).Get(req.Request.Context(), crdName); err == nil {
				crdHandler(res.ResponseWriter, req.Request)
				return
			}

			// fall-through to the native types
		}

		// Fall back to pass through if we didn't match anything above
		chain.ProcessFilter(req, res)
	}
}

func serveCoreV1Discovery(ctx context.Context, crdLister kcp.ClusterAwareCRDClusterLister, coreHandler func(w http.ResponseWriter, req *http.Request), res http.ResponseWriter, req *http.Request) {
	clusterName, wildcard, err := request.ClusterNameOrWildcardFrom(ctx)
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("no cluster found in the context")),
			errorCodecs, schema.GroupVersion{}, res, req,
		)
		return
	}
	if wildcard {
		// this is the only case where wildcard works for a list because this is our special CRD lister that handles it.
		clusterName = "*"
	}

	// Get all the CRDs to see if any of them are in v1
	crds, err := crdLister.Cluster(clusterName).List(ctx, labels.Everything())
	if err != nil {
		// Listing from a lister can really only ever fail if invoking meta.Accessor() on an item in the list fails.
		// Which means it essentially will never fail. But just in case...
		err = apierrors.NewInternalError(fmt.Errorf("unable to serve /api/v1 discovery: error listing CustomResourceDefinitions: %w", err))
		_ = responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, res, req)
		return
	}

	// Generate discovery for the CRDs.
	crdDiscovery := apiextensionsapiserver.APIResourcesForGroupVersion("", "v1", crds)

	// v1 CRDs present - need to clone the request, add our passthrough header, and get /api/v1 discovery from
	// the GenericControlPlane's server.
	cr := utilnet.CloneRequest(req)
	cr.Header.Add(passthroughHeader, "1")

	writer := newInMemoryResponseWriter()
	coreHandler(writer, cr)
	if writer.respCode != http.StatusOK {
		// Write the response back to the client
		res.WriteHeader(writer.respCode)
		res.Write(writer.data) //nolint:errcheck
		return
	}

	// Decode the response. Have to pass into correctly (instead of nil) because APIResourceList
	// is "special" - it doesn't have an apiVersion field that the decoder needs to determine the
	// type.
	into := &metav1.APIResourceList{}
	obj, _, err := miniaggregator.DiscoveryCodecs.UniversalDeserializer().Decode(writer.data, nil, into)
	if err != nil {
		err = apierrors.NewInternalError(fmt.Errorf("unable to serve /api/v1 discovery: error decoding /api/v1 response from generic control plane: %w", err))
		_ = responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, res, req)
		return
	}
	v1ResourceList, ok := obj.(*metav1.APIResourceList)
	if !ok {
		err = apierrors.NewInternalError(fmt.Errorf("unable to serve /api/v1 discovery: error decoding /api/v1 response from generic control plane: unexpected data type %T", obj))
		_ = responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, res, req)
		return
	}

	// Combine the 2 sets of discovery resources
	v1ResourceList.APIResources = append(v1ResourceList.APIResources, crdDiscovery...)

	// Sort based on resource name
	sort.SliceStable(v1ResourceList.APIResources, func(i, j int) bool {
		return v1ResourceList.APIResources[i].Name < v1ResourceList.APIResources[j].Name
	})

	// Serve up our combined discovery
	versionHandler := apiserverdiscovery.NewAPIVersionHandler(miniaggregator.DiscoveryCodecs, schema.GroupVersion{Group: "", Version: "v1"}, apiserverdiscovery.APIResourceListerFunc(func() []metav1.APIResource {
		return v1ResourceList.APIResources
	}))
	versionHandler.ServeHTTP(res, req)
}

// COPIED FROM kube-aggregator
// inMemoryResponseWriter is a http.Writer that keep the response in memory.
type inMemoryResponseWriter struct {
	writeHeaderCalled bool
	header            http.Header
	respCode          int
	data              []byte
}

func newInMemoryResponseWriter() *inMemoryResponseWriter {
	return &inMemoryResponseWriter{header: http.Header{}}
}

func (r *inMemoryResponseWriter) Header() http.Header {
	return r.header
}

func (r *inMemoryResponseWriter) WriteHeader(code int) {
	r.writeHeaderCalled = true
	r.respCode = code
}

func (r *inMemoryResponseWriter) Write(in []byte) (int, error) {
	if !r.writeHeaderCalled {
		r.WriteHeader(http.StatusOK)
	}
	r.data = append(r.data, in...)
	return len(in), nil
}

func (r *inMemoryResponseWriter) String() string {
	s := fmt.Sprintf("ResponseCode: %d", r.respCode)
	if r.data != nil {
		s += fmt.Sprintf(", Body: %s", string(r.data))
	}
	if r.header != nil {
		s += fmt.Sprintf(", Header: %s", r.header)
	}
	return s
}
