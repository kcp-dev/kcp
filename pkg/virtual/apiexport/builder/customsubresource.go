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

package builder

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	apiuser "k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"k8s.io/klog/v2"
	"k8s.io/streaming/pkg/httpstream"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	"github.com/kcp-dev/kcp/pkg/proxy/authheaders"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
)

// methodForVerb maps a verb to the HTTP method a request for it arrives with.
//
// A custom subresource is reached as a Connecter, which is authorized under the
// verb derived from the method rather than under a literal "connect": a POST to
// widgets/frobnicate needs "create" on it, the way kubectl exec needs "create"
// on pods/exec. Going back the other way is what lets a claim's verbs decide
// which methods the subresource accepts.
var methodForVerb = map[string]string{
	"create": http.MethodPost,
	"delete": http.MethodDelete,
	"get":    http.MethodGet,
	"patch":  http.MethodPatch,
	"update": http.MethodPut,
}

// shardProxy reaches the shard on behalf of a request served here.
//
// The transports are built once and shared: a custom subresource request differs
// from the next only in who is asking, which is impersonation applied per
// request, so nothing about it needs its own connection pool.
type shardProxy struct {
	host *url.URL

	// transport carries ordinary requests. upgradeTransport carries protocol
	// upgrades, and is pinned to HTTP/1.1 because an Upgrade header does not
	// survive an HTTP/2 connection -- a streaming subresource on a negotiated
	// HTTP/2 connection fails in a way that looks like the backend refusing it.
	transport        http.RoundTripper
	upgradeTransport http.RoundTripper
}

func newShardProxy(cfg *restclient.Config) (*shardProxy, error) {
	host, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("error parsing shard URL %q: %w", cfg.Host, err)
	}

	tr, err := restclient.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("error building transport to the shard: %w", err)
	}

	upgradeCfg := restclient.CopyConfig(cfg)
	upgradeCfg.NextProtos = []string{"http/1.1"}
	upgradeTr, err := restclient.TransportFor(upgradeCfg)
	if err != nil {
		return nil, fmt.Errorf("error building upgrade transport to the shard: %w", err)
	}

	return &shardProxy{host: host, transport: tr, upgradeTransport: upgradeTr}, nil
}

// customSubresourceStorage serves a custom subresource by proxying to the shard.
//
// The subresource is not served here and not served by the shard either: the
// shard resolves the APIExport entry to the virtual workspace the provider
// advertises for it and forwards there. So this storage only has to put the
// request back on the shard under the resource it belongs to, which is the same
// move the forwarding storage makes for the resource itself.
//
// It proxies rather than forwarding through a typed client because a custom
// subresource may be anything: a JSON body, a stream, an upgraded connection.
// A client that decodes the body would decide that here, and the declaration
// says nothing that would let it.
type customSubresourceStorage struct {
	// parent is the resource the subresource hangs off, as served here.
	parent          schema.GroupVersionResource
	subresource     string
	kind            schema.GroupVersionKind
	namespaceScoped bool

	// methods are the HTTP methods this subresource accepts, derived from the
	// verbs it is served under.
	methods []string

	// identities reports the identity hashes the parent is stored under on this
	// shard, so that the forwarded request names the same resource the forwarding
	// storage would name for the parent itself.
	identities forwardingregistry.IdentityHashesFunc

	// warrant is the warrant to add to the impersonated user, which is what
	// gives the forwarded request access to a claimed resource in a consumer
	// workspace.
	warrant func(cluster logicalcluster.Name) (string, error)

	proxy *shardProxy
}

var (
	_ rest.Storage   = (*customSubresourceStorage)(nil)
	_ rest.Connecter = (*customSubresourceStorage)(nil)
)

func newCustomSubresourceStorage(
	parent schema.GroupVersionResource,
	sub apireconciler.CustomSubresource,
	namespaceScoped bool,
	identities forwardingregistry.IdentityHashesFunc,
	warrant func(cluster logicalcluster.Name) (string, error),
	proxy *shardProxy,
) *customSubresourceStorage {
	methods := sets.New[string]()
	for _, verb := range sub.Verbs {
		if verb == "*" {
			for _, method := range methodForVerb {
				methods.Insert(method)
			}
			continue
		}
		if method, ok := methodForVerb[verb]; ok {
			methods.Insert(method)
		}
	}

	return &customSubresourceStorage{
		parent:          parent,
		subresource:     sub.Name,
		kind:            sub.Kind,
		namespaceScoped: namespaceScoped,
		methods:         sets.List(methods), // sorted, so discovery does not change between identical builds
		identities:      identities,
		warrant:         warrant,
		proxy:           proxy,
	}
}

func (s *customSubresourceStorage) New() runtime.Object {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(s.kind)
	return obj
}

func (s *customSubresourceStorage) Destroy() {}

func (s *customSubresourceStorage) ConnectMethods() []string {
	return s.methods
}

// NewConnectOptions returns no options object: the query string is passed on to
// the endpoint unread, because what it means is the endpoint's to define.
func (s *customSubresourceStorage) NewConnectOptions() (runtime.Object, bool, string) {
	return nil, false, ""
}

func (s *customSubresourceStorage) Connect(ctx context.Context, name string, _ runtime.Object, responder rest.Responder) (http.Handler, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, apierrors.NewBadRequest(err.Error())
	}
	if cluster.Wildcard {
		// A subresource is reached through one named object in one workspace.
		// Wildcard requests also cannot be impersonated against a concrete
		// cluster, so there is nothing to forward them as.
		return nil, apierrors.NewBadRequest(fmt.Sprintf("%s is a subresource of a single object and cannot be requested across all logical clusters", s.subresource))
	}

	user, found := genericapirequest.UserFrom(ctx)
	if !found {
		return nil, apierrors.NewInternalError(errors.New("no user in context"))
	}

	warrant, err := s.warrant(cluster.Name)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	target := *s.proxy.host
	target.Path = path.Join(s.proxy.host.Path, s.targetPath(ctx, cluster.Name, name))

	return &proxyHandler{
		storage: s,
		target:  &target,
		user:    user,
		warrant: warrant,

		responder: responder,
	}, nil
}

// targetPath is where the subresource lives on the shard.
//
// The parent carries its identity hash where exactly one is served here, the
// same rule the forwarding storage applies: with no hash, or more than one, the
// plain resource is forwarded and the shard resolves the identity from the
// APIBinding in the target workspace.
func (s *customSubresourceStorage) targetPath(ctx context.Context, cluster logicalcluster.Name, name string) string {
	resource := s.parent.Resource
	if s.identities != nil {
		if hashes := s.identities(ctx); len(hashes) == 1 {
			resource += ":" + hashes[0]
		}
	}

	parts := []string{"/clusters", cluster.String()}
	if s.parent.Group == "" {
		parts = append(parts, "api", s.parent.Version)
	} else {
		parts = append(parts, "apis", s.parent.Group, s.parent.Version)
	}
	if s.namespaceScoped {
		namespace, _ := genericapirequest.NamespaceFrom(ctx)
		parts = append(parts, "namespaces", namespace)
	}
	parts = append(parts, resource, name, s.subresource)

	return path.Join(parts...)
}

// proxyHandler forwards one request to the shard.
type proxyHandler struct {
	storage *customSubresourceStorage
	target  *url.URL
	user    apiuser.Info
	warrant string

	responder rest.Responder
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The transport is chosen from the request rather than from the declaration:
	// a client asking to upgrade is exactly the condition under which HTTP/2
	// breaks the Upgrade header, and it needs no field on the API to say so.
	base := h.storage.proxy.transport
	if httpstream.IsUpgradeRequest(r) {
		base = h.storage.proxy.upgradeTransport
	}

	target := *h.target
	target.RawQuery = r.URL.RawQuery

	proxy := &httputil.ReverseProxy{
		Transport: transport.NewImpersonatingRoundTripper(impersonationHeaders(h.user, h.warrant), base),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = &target
			pr.Out.Host = ""
			clearInboundCredentials(pr.Out.Header)
			pr.SetXForwarded()
		},
		ErrorHandler: func(_ http.ResponseWriter, req *http.Request, err error) {
			klog.FromContext(req.Context()).V(2).Info("error proxying custom subresource to the shard",
				"subresource", h.storage.subresource, "url", h.target.String(), "err", err.Error())
			h.responder.Error(apierrors.NewServiceUnavailable(fmt.Sprintf("error reaching %s: %v", h.storage.subresource, err)))
		},
	}

	proxy.ServeHTTP(w, r)
}

// clearInboundCredentials removes every way the client could speak for itself on
// the hop to the shard.
//
// The client authenticated to this virtual workspace; the shard is reached as
// this virtual workspace, impersonating that client. Passing the client's own
// credentials on would defeat both halves of that. The bearer token would
// authenticate the request as the client, which client-go then refuses to
// overwrite with the configured credentials, and the shard would see a user
// without permission to impersonate. An inbound Impersonate-User is taken by
// client-go as impersonation already applied, so it would replace ours. And the
// request-header identity headers are believed over a connection whose client
// certificate the shard's --requestheader-client-ca-file trusts, which this one
// is.
func clearInboundCredentials(header http.Header) {
	header.Del("Authorization")

	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), "impersonate-") {
			header.Del(key)
		}
	}

	authheaders.ClearAuthHeaders(header,
		authheaders.DefaultUserHeader, authheaders.DefaultGroupHeader, authheaders.DefaultExtraHeaderPrefix)
}

// impersonationHeaders is the transport-level spelling of the impersonation the
// forwarding storage applies through its client: the requesting user plus a
// warrant granting access in the target workspace.
func impersonationHeaders(u apiuser.Info, warrants ...string) transport.ImpersonationConfig {
	cfg := newImpersonationConfig(u, warrants...)
	return transport.ImpersonationConfig{
		UserName: cfg.UserName,
		UID:      cfg.UID,
		Groups:   cfg.Groups,
		Extra:    cfg.Extra,
	}
}
