/*
Copyright 2021 The kcp Authors.

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

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"

	_ "net/http/pprof"
)

var (
	// KcpRootGroupExportNames lists the APIExports in the root workspace for standard kcp groups.
	KcpRootGroupExportNames = map[string]string{
		"tenancy.kcp.io":  "tenancy.kcp.io",
		"topology.kcp.io": "topology.kcp.io",
	}

	// KcpRootGroupResourceExportNames lists the APIExports in the root workspace for standard kcp group resources.
	KcpRootGroupResourceExportNames = map[schema.GroupResource]string{
		{Group: "core.kcp.io", Resource: "shards"}:                          "shards.core.kcp.io",
		{Group: "migration.kcp.io", Resource: "logicalclustermigrations"}:   "migration.kcp.io",
		{Group: "migration.kcp.io", Resource: "apiexportidentityrotations"}: "migration.kcp.io",
	}
)

// IdentityRefreshInterval is how often the resolved identities are compared
// with the APIExports in the root workspace after the initial resolution.
// TODO(mjudeikis): I'm not sure if 10s is the right interval for refreshing identities.
// We need to evaluate the performance and correctness implications.
var IdentityRefreshInterval = 10 * time.Second

type identities struct {
	lock sync.RWMutex

	groupIdentities         map[string]string
	groupResourceIdentities map[schema.GroupResource]string

	// watches are the open watch responses that were sent with an injected
	// identity, by that identity. When the identity changes (e.g. after an
	// APIExport identity rotation) they are expired, so their informers
	// relist with the new identity.
	watchesLock sync.Mutex
	watches     map[string]map[*expirableWatchBody]struct{}
}

func newIdentities(groupExportNames map[string]string, groupResourceExportNames map[schema.GroupResource]string) *identities {
	ids := &identities{
		groupIdentities:         map[string]string{},
		groupResourceIdentities: map[schema.GroupResource]string{},
		watches:                 map[string]map[*expirableWatchBody]struct{}{},
	}
	for group := range groupExportNames {
		ids.groupIdentities[group] = ""
	}
	for gr := range groupResourceExportNames {
		ids.groupResourceIdentities[gr] = ""
	}
	return ids
}

// trackWatch registers an open watch response sent with the given identity.
func (ids *identities) trackWatch(identity string, body *expirableWatchBody) {
	ids.watchesLock.Lock()
	defer ids.watchesLock.Unlock()
	if ids.watches[identity] == nil {
		ids.watches[identity] = map[*expirableWatchBody]struct{}{}
	}
	ids.watches[identity][body] = struct{}{}
}

func (ids *identities) untrackWatch(identity string, body *expirableWatchBody) {
	ids.watchesLock.Lock()
	defer ids.watchesLock.Unlock()
	delete(ids.watches[identity], body)
	if len(ids.watches[identity]) == 0 {
		delete(ids.watches, identity)
	}
}

// expireWatches expires every open watch that was sent with one of the given
// identities.
func (ids *identities) expireWatches(identities ...string) int {
	ids.watchesLock.Lock()
	var bodies []*expirableWatchBody
	for _, identity := range identities {
		for body := range ids.watches[identity] {
			bodies = append(bodies, body)
		}
	}
	ids.watchesLock.Unlock()

	for _, body := range bodies {
		body.expire()
	}
	return len(bodies)
}

func (ids *identities) grIdentity(gr schema.GroupResource) (string, bool) {
	ids.lock.RLock()
	defer ids.lock.RUnlock()

	if id, found := ids.groupResourceIdentities[gr]; found {
		return id, true
	}
	if id, found := ids.groupIdentities[gr.Group]; found {
		return id, true
	}
	return "", false
}

// NewConfigWithWildcardIdentities creates a rest.Config with injected resource identities for
// individual group or group resources. Each group or resource is coming from one APIExport whose
// names are passed in as a map.
//
// The returned resolve function will get the APIExports and extract the identities.
// The resolve func might return an error if the APIExport is not found or for other reason. Only
// after it succeeds a client using the returned rest.Config can use the group and group resources
// with identities.
//
// The returned refresh function keeps the identities up to date until its context is done, e.g.
// across an APIExport identity rotation. Start it after resolve succeeded.
func NewConfigWithWildcardIdentities(config *rest.Config,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) (identityConfig *rest.Config, resolve func(ctx context.Context) error, refresh func(ctx context.Context)) {
	identityRoundTripper, identityResolver, identityRefresher := NewWildcardIdentitiesWrappingRoundTripper(groupExportNames, groupResourceExportNames, config, localShardKubeClusterClient)
	identityConfig = rest.CopyConfig(config)
	identityConfig.Wrap(identityRoundTripper)
	return identityConfig, identityResolver, identityRefresher
}

// NewWildcardIdentitiesWrappingRoundTripper creates an HTTP RoundTripper
// that injects resource identities for individual groups or group resources.
// Each group or resource is coming from one APIExport whose names are passed in as a map.
// The RoundTripper is exposed as a function that allows wrapping the RoundTripper
//
// The method also returns the resolve function that gets the APIExports and extract the identities.
// The resolve func might return an error if the APIExport is not found or for other reason. Only
// after it succeeds a client using the returned RoundTripper can use the group and group resources
// with identities.
//
// The returned refresh function keeps the identities up to date until its context is done, e.g.
// across an APIExport identity rotation. Start it after resolve succeeded.
func NewWildcardIdentitiesWrappingRoundTripper(groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	config *rest.Config,
	localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) (wrap func(rt http.RoundTripper) http.RoundTripper, resolve func(ctx context.Context) error, refresh func(ctx context.Context)) {
	ids := newIdentities(groupExportNames, groupResourceExportNames)
	provider := apiExportIdentityProvider(config, localShardKubeClusterClient)
	return injectKcpIdentities(ids),
		wildcardIdentitiesResolver(ids, groupExportNames, groupResourceExportNames, provider),
		wildcardIdentitiesRefresher(ids, groupExportNames, groupResourceExportNames, provider)
}

// wildcardIdentitiesRefresher returns a function that, until the context is
// done, periodically compares the resolved identities with the APIExports
// and swaps those that changed, e.g. because the export's identity was
// rotated. Open watches sent with a replaced identity are expired so their
// informers relist with the new one. It is meant to be started after the
// resolve function succeeded.
func wildcardIdentitiesRefresher(ids *identities,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	apiExportIdentityProviderFn func(ctx context.Context, apiExportName string) (string, error)) func(ctx context.Context) {
	return func(ctx context.Context) {
		wait.UntilWithContext(ctx, func(ctx context.Context) {
			refreshIdentities(ctx, ids, groupExportNames, groupResourceExportNames, apiExportIdentityProviderFn)
		}, IdentityRefreshInterval)
	}
}

// refreshIdentities re-resolves every identity and replaces those that
// changed. Exports that cannot be resolved keep their current identity.
func refreshIdentities(ctx context.Context, ids *identities,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	apiExportIdentityProviderFn func(ctx context.Context, apiExportName string) (string, error)) {
	logger := klog.FromContext(ctx)

	// resolve each export once, even if it serves several groups or resources.
	current := map[string]string{}
	resolve := func(name string) (string, bool) {
		if id, ok := current[name]; ok {
			return id, id != ""
		}
		id, err := apiExportIdentityProviderFn(ctx, name)
		if err != nil {
			logger.V(4).Info("failed to refresh APIExport identity, keeping the current one", "apiExport", name, "err", err)
			id = ""
		}
		current[name] = id
		return id, id != ""
	}

	var replaced []string
	swap := func(old *string, name, id string) {
		if *old == id {
			return
		}
		logger.Info("APIExport identity changed, replacing it", "apiExport", name, "oldIdentity", *old, "identity", id)
		if *old != "" {
			replaced = append(replaced, *old)
		}
		*old = id
	}

	for group, name := range groupExportNames {
		if id, ok := resolve(name); ok {
			ids.lock.Lock()
			old := ids.groupIdentities[group]
			swap(&old, name, id)
			ids.groupIdentities[group] = old
			ids.lock.Unlock()
		}
	}
	for gr, name := range groupResourceExportNames {
		if id, ok := resolve(name); ok {
			ids.lock.Lock()
			old := ids.groupResourceIdentities[gr]
			swap(&old, name, id)
			ids.groupResourceIdentities[gr] = old
			ids.lock.Unlock()
		}
	}

	if len(replaced) > 0 {
		n := ids.expireWatches(replaced...)
		logger.Info("expired watches using replaced APIExport identities", "count", n)
	}
}

func wildcardIdentitiesResolver(ids *identities,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	apiExportIdentityProviderFn func(ctx context.Context, apiExportName string) (string, error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		logger := klog.FromContext(ctx)
		var errs []error
		for group, name := range groupExportNames {
			logger := logging.WithObject(logger, &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: map[string]string{logicalcluster.AnnotationKey: core.RootCluster.String()},
				},
			}).WithValues("group", group)
			ids.lock.RLock()
			id := ids.groupIdentities[group]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExportIdentity, err := apiExportIdentityProviderFn(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExportIdentity == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}
			logger = logger.WithValues("identity", apiExportIdentity)

			ids.lock.Lock()
			ids.groupIdentities[group] = apiExportIdentity
			ids.lock.Unlock()

			logger.V(4).Info("APIExport has identity")
		}
		for gr, name := range groupResourceExportNames {
			logger := logging.WithObject(logger, &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:        name,
					Annotations: map[string]string{logicalcluster.AnnotationKey: core.RootCluster.String()},
				},
			}).WithValues("gr", gr.String())
			ids.lock.RLock()
			id := ids.groupResourceIdentities[gr]
			ids.lock.RUnlock()

			if id != "" {
				continue
			}

			apiExportIdentity, err := apiExportIdentityProviderFn(ctx, name)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if apiExportIdentity == "" {
				errs = append(errs, fmt.Errorf("APIExport %s is not ready", name))
				continue
			}
			logger = logger.WithValues("identity", apiExportIdentity)

			ids.lock.Lock()
			ids.groupResourceIdentities[gr] = apiExportIdentity
			ids.lock.Unlock()

			logger.V(4).Info("APIExport has identity")
		}
		return utilerrors.NewAggregate(errs)
	}
}

func apiExportIdentityProvider(config *rest.Config, localShardKubeClusterClient kcpkubernetesclientset.ClusterInterface) func(ctx context.Context, apiExportName string) (string, error) {
	return func(ctx context.Context, apiExportName string) (string, error) {
		rootShardKcpClient, err := kcpclientset.NewForConfig(config)
		if err != nil {
			return "", err
		}

		if localShardKubeClusterClient != nil {
			apiExportIdentitiesConfigMap, err := localShardKubeClusterClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1().ConfigMaps("default").Get(ctx, identitycache.ConfigMapName, metav1.GetOptions{})
			if err == nil {
				apiExportIdentity, found := apiExportIdentitiesConfigMap.Data[apiExportName]
				if found {
					return apiExportIdentity, nil
				}
			}
			// fall back when:
			// - this is not the root shard (localShardKubeClusterClient == nil)
			// - the cm wasn't found
			// - an entry in the cm wasn't found
		}
		apiExport, err := rootShardKcpClient.Cluster(core.RootCluster.Path()).ApisV1alpha2().APIExports().Get(ctx, apiExportName, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return apiExport.Status.IdentityHash, nil
	}
}

type roundTripperFunc struct {
	delegate http.RoundTripper
	fn       func(*http.Request) (*http.Response, error)
}

var _ http.RoundTripper = roundTripperFunc{}

func (rt roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return rt.fn(r)
}

func (rt roundTripperFunc) WrappedRoundTripper() http.RoundTripper {
	return rt.delegate
}

// injectKcpIdentities injects the kcp identities into the request URLs.
func injectKcpIdentities(ids *identities) func(rt http.RoundTripper) http.RoundTripper {
	return func(rt http.RoundTripper) http.RoundTripper {
		return roundTripperFunc{
			delegate: rt,
			fn: func(origReq *http.Request) (*http.Response, error) {
				urlPath, identity, err := decorateWildcardPathsWithResourceIdentities(origReq.URL.Path, ids)
				if err != nil {
					return nil, err
				}
				if urlPath == origReq.URL.Path && identity == "" {
					return rt.RoundTrip(origReq)
				}

				req := origReq
				if urlPath != origReq.URL.Path {
					req = new(http.Request)
					*req = *origReq // shallow copy
					req.URL = &url.URL{}
					*req.URL = *origReq.URL
					req.URL.Path = urlPath
				}

				resp, err := rt.RoundTrip(req)
				if err != nil || identity == "" || !isWatch(req) || resp.StatusCode != http.StatusOK || resp.Body == nil {
					return resp, err
				}
				body := newExpirableWatchBody(resp.Body, isJSON(resp.Header.Get("Content-Type")))
				body.onClose = func() { ids.untrackWatch(identity, body) }
				ids.trackWatch(identity, body)
				resp.Body = body
				return resp, nil
			},
		}
	}
}

// decorateWildcardPathsWithResourceIdentities adds per-API-group identity to wildcard URL paths, e.g.
//
//	/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces/root
//
// becomes
//
//	/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:<identity>/root
//
// It also returns the identity of the path, empty for paths without one.
func decorateWildcardPathsWithResourceIdentities(urlPath string, ids *identities) (string, string, error) {
	// Check for: /clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces/root
	if !strings.HasPrefix(urlPath, "/clusters/*/apis/") {
		return urlPath, "", nil
	}

	comps := strings.SplitN(strings.TrimPrefix(urlPath, "/"), "/", 7)
	if len(comps) < 6 {
		return urlPath, "", nil
	}

	// It's possible the outgoing request already has an identity specified. Make sure we exclude that when
	// determining the resource in question.
	parts := strings.SplitN(comps[5], ":", 2)
	if len(parts) == 0 {
		return "", "", fmt.Errorf("invalid resource %q", comps[5])
	}

	resource := parts[0]

	gr := schema.GroupResource{Group: comps[3], Resource: resource}
	if id, found := ids.grIdentity(gr); found && gr != corev1alpha1.Resource("logicalclusters") {
		if len(id) == 0 {
			return "", "", fmt.Errorf("identity for %s is unknown", gr)
		}

		passedInIdentity := ""
		if len(parts) > 1 {
			passedInIdentity = parts[1]
		}

		if passedInIdentity != "" && passedInIdentity != id {
			return "", "", fmt.Errorf("invalid identity %q for resource %q", passedInIdentity, resource)
		}

		comps[5] = resource + ":" + id

		return "/" + path.Join(comps...), id, nil
	}

	return urlPath, "", nil
}

func isWatch(req *http.Request) bool {
	switch req.URL.Query().Get("watch") {
	case "true", "1":
		return true
	}
	return false
}

func isJSON(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.TrimSpace(mediaType) == "application/json"
}

// expiredWatchEvent is a JSON watch event carrying a 410 Expired status. A
// reflector that receives it ends its watch and relists, unlike a watch that
// just ends, after which it resumes watching from the last resource version.
var expiredWatchEvent = func() []byte {
	bs, err := json.Marshal(struct {
		Type   string        `json:"type"`
		Object metav1.Status `json:"object"`
	}{
		Type: "ERROR",
		Object: metav1.Status{
			TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status:   metav1.StatusFailure,
			Message:  "the APIExport identity of this resource changed",
			Reason:   metav1.StatusReasonExpired,
			Code:     http.StatusGone,
		},
	})
	if err != nil {
		panic(err)
	}
	return append(bs, '\n')
}()

// expirableWatchBody wraps the body of a watch response so the watch can be
// ended with a 410 Expired error event. The event is only appended to JSON
// streams, and only between two events; otherwise the stream just ends.
type expirableWatchBody struct {
	io.ReadCloser
	json    bool
	onClose func()

	lock sync.Mutex
	// expired is set by expire. The next read error of the underlying body
	// (closing it unblocks a pending read) ends the stream.
	expired bool
	// atBoundary is true while the bytes returned so far end with a
	// complete event.
	atBoundary bool
	pending    []byte
	done       bool
}

func newExpirableWatchBody(body io.ReadCloser, json bool) *expirableWatchBody {
	return &expirableWatchBody{ReadCloser: body, json: json, atBoundary: true}
}

func (b *expirableWatchBody) Read(p []byte) (int, error) {
	b.lock.Lock()
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		b.lock.Unlock()
		return n, nil
	}
	if b.done {
		b.lock.Unlock()
		return 0, io.EOF
	}
	b.lock.Unlock()

	n, err := b.ReadCloser.Read(p)

	b.lock.Lock()
	defer b.lock.Unlock()
	if n > 0 {
		b.atBoundary = p[n-1] == '\n'
	}
	if err == nil || !b.expired {
		return n, err
	}

	b.done = true
	if b.json && b.atBoundary {
		b.pending = expiredWatchEvent
	}
	if n > 0 {
		return n, nil
	}
	if len(b.pending) > 0 {
		m := copy(p, b.pending)
		b.pending = b.pending[m:]
		return m, nil
	}
	return 0, io.EOF
}

// expire ends the watch: the underlying body is closed, and the reader gets
// the expired event (if possible) followed by EOF.
func (b *expirableWatchBody) expire() {
	b.lock.Lock()
	if b.expired {
		b.lock.Unlock()
		return
	}
	b.expired = true
	b.lock.Unlock()
	_ = b.ReadCloser.Close()
}

func (b *expirableWatchBody) Close() error {
	if b.onClose != nil {
		b.onClose()
	}
	return b.ReadCloser.Close()
}
