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
	"sort"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

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
		{Group: "core.kcp.io", Resource: "shards"}:                        "shards.core.kcp.io",
		{Group: "migration.kcp.io", Resource: "logicalclustermigrations"}: "migration.kcp.io",
	}
)

// ResolvedIdentity is the identity of one core APIExport together with where
// it came from.
type ResolvedIdentity struct {
	// Hash is the identity hash as it appears in request URLs.
	Hash string
	// Source says where the identity came from.
	Source adminv1alpha1.CoreIdentitySource
	// KeyID and Derivation record which key and derivation produced the
	// identity. Empty unless Source is Key.
	KeyID      string
	Derivation string
}

// Classifier can refine the source of a resolved identity, e.g. recognize a
// legacy-looking hash as one that was in fact derived from the key.
type Classifier func(exportName string, id ResolvedIdentity) ResolvedIdentity

// Identities holds the identities of the core APIExports, filled in by
// Resolve and injected into wildcard request URLs by the round tripper.
// Resolve never replaces an identity; the APIExportHandler does, when the
// APIExport's identity was rotated.
type Identities struct {
	lock sync.RWMutex

	groupExportNames         map[string]string
	groupResourceExportNames map[schema.GroupResource]string

	groupIdentities         map[string]string
	groupResourceIdentities map[schema.GroupResource]string
	byExport                map[string]ResolvedIdentity

	classifier Classifier

	// watches are the open watch responses that were sent with an injected
	// identity, by that identity. When the identity is replaced (e.g. after
	// an APIExport identity rotation) they are expired, so their informers
	// relist with the new identity.
	watchesLock sync.Mutex
	watches     map[string]map[*expirableWatchBody]struct{}

	// listeners are called after an identity was replaced.
	listenersLock sync.Mutex
	listeners     []func()
}

// NewIdentities creates an empty Identities for the given exports. Each
// group or group resource is served by one APIExport in the root workspace,
// whose name is the map value.
func NewIdentities(groupExportNames map[string]string, groupResourceExportNames map[schema.GroupResource]string) *Identities {
	ids := &Identities{
		groupExportNames:         groupExportNames,
		groupResourceExportNames: groupResourceExportNames,
		groupIdentities:          map[string]string{},
		groupResourceIdentities:  map[schema.GroupResource]string{},
		byExport:                 map[string]ResolvedIdentity{},
		watches:                  map[string]map[*expirableWatchBody]struct{}{},
	}
	for group := range groupExportNames {
		ids.groupIdentities[group] = ""
	}
	for gr := range groupResourceExportNames {
		ids.groupResourceIdentities[gr] = ""
	}
	return ids
}

// SetClassifier installs a Classifier applied to every identity on
// resolution.
func (ids *Identities) SetClassifier(c Classifier) {
	ids.lock.Lock()
	defer ids.lock.Unlock()
	ids.classifier = c
}

// ExportNames returns the sorted, de-duplicated names of the APIExports
// these identities are for.
func (ids *Identities) ExportNames() []string {
	names := map[string]struct{}{}
	for _, name := range ids.groupExportNames {
		names[name] = struct{}{}
	}
	for _, name := range ids.groupResourceExportNames {
		names[name] = struct{}{}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// Snapshot returns the identities resolved so far, by APIExport name.
func (ids *Identities) Snapshot() map[string]ResolvedIdentity {
	ids.lock.RLock()
	defer ids.lock.RUnlock()
	result := make(map[string]ResolvedIdentity, len(ids.byExport))
	for name, id := range ids.byExport {
		result[name] = id
	}
	return result
}

// Resolved reports whether every export has an identity.
func (ids *Identities) Resolved() bool {
	ids.lock.RLock()
	defer ids.lock.RUnlock()
	for _, name := range ids.ExportNames() {
		if _, ok := ids.byExport[name]; !ok {
			return false
		}
	}
	return true
}

func (ids *Identities) grIdentity(gr schema.GroupResource) (string, bool) {
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

// Resolve fills in every identity that is still missing using the provider.
// It returns an aggregate error when at least one identity is still
// unknown afterwards; identities that were found are kept, so calling it
// again only asks for the rest.
func (ids *Identities) Resolve(ctx context.Context, provider Provider) error {
	logger := klog.FromContext(ctx)
	var errs []error

	resolve := func(exportName string) (ResolvedIdentity, bool) {
		ids.lock.RLock()
		id, found := ids.byExport[exportName]
		classifier := ids.classifier
		ids.lock.RUnlock()
		if found {
			return id, true
		}

		id, err := provider(ctx, exportName)
		if err != nil {
			errs = append(errs, fmt.Errorf("APIExport %s: %w", exportName, err))
			return ResolvedIdentity{}, false
		}
		if id.Hash == "" {
			errs = append(errs, fmt.Errorf("APIExport %s is not ready", exportName))
			return ResolvedIdentity{}, false
		}
		if classifier != nil {
			id = classifier(exportName, id)
		}

		ids.lock.Lock()
		defer ids.lock.Unlock()
		if existing, found := ids.byExport[exportName]; found {
			return existing, true
		}
		ids.byExport[exportName] = id
		logger.V(4).Info("APIExport has identity", "export", exportName, "identity", id.Hash, "source", id.Source)
		return id, true
	}

	for group, name := range ids.groupExportNames {
		if id, ok := resolve(name); ok {
			ids.lock.Lock()
			ids.groupIdentities[group] = id.Hash
			ids.lock.Unlock()
		}
	}
	for gr, name := range ids.groupResourceExportNames {
		if id, ok := resolve(name); ok {
			ids.lock.Lock()
			ids.groupResourceIdentities[gr] = id.Hash
			ids.lock.Unlock()
		}
	}
	return utilerrors.NewAggregate(errs)
}

// AddChangeListener registers a function that is called after an identity
// was replaced.
func (ids *Identities) AddChangeListener(fn func()) {
	ids.listenersLock.Lock()
	defer ids.listenersLock.Unlock()
	ids.listeners = append(ids.listeners, fn)
}

// set makes hash the identity of the named APIExport and of every group and
// group resource it serves, e.g. after the export's identity was rotated.
// When that replaces an identity that was in use, the open watches sent with
// the old identity are expired, so their informers relist with the new one,
// and the change listeners are called.
func (ids *Identities) set(exportName, hash string) {
	ids.lock.Lock()
	old, found := ids.byExport[exportName]
	if found && old.Hash == hash {
		ids.lock.Unlock()
		return
	}
	id := legacy(hash)
	if ids.classifier != nil {
		id = ids.classifier(exportName, id)
	}
	ids.byExport[exportName] = id
	for group, name := range ids.groupExportNames {
		if name == exportName {
			ids.groupIdentities[group] = hash
		}
	}
	for gr, name := range ids.groupResourceExportNames {
		if name == exportName {
			ids.groupResourceIdentities[gr] = hash
		}
	}
	ids.lock.Unlock()

	logger := klog.Background().WithValues("apiExport", exportName, "identity", hash, "source", id.Source)
	if !found {
		logger.V(2).Info("APIExport has identity")
		return
	}
	logger.Info("APIExport identity changed, replacing it", "oldIdentity", old.Hash)
	n := ids.expireWatches(old.Hash)
	logger.Info("expired watches using the replaced APIExport identity", "count", n)

	ids.listenersLock.Lock()
	listeners := append([]func(){}, ids.listeners...)
	ids.listenersLock.Unlock()
	for _, fn := range listeners {
		fn()
	}
}

// trackWatch registers an open watch response sent with the given identity.
func (ids *Identities) trackWatch(identity string, body *expirableWatchBody) {
	ids.watchesLock.Lock()
	defer ids.watchesLock.Unlock()
	if ids.watches[identity] == nil {
		ids.watches[identity] = map[*expirableWatchBody]struct{}{}
	}
	ids.watches[identity][body] = struct{}{}
}

func (ids *Identities) untrackWatch(identity string, body *expirableWatchBody) {
	ids.watchesLock.Lock()
	defer ids.watchesLock.Unlock()
	delete(ids.watches[identity], body)
	if len(ids.watches[identity]) == 0 {
		delete(ids.watches, identity)
	}
}

// expireWatches expires every open watch that was sent with one of the given
// identities.
func (ids *Identities) expireWatches(identities ...string) int {
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

// APIExportHandler returns an informer event handler that keeps the
// identities in sync with status.identityHash of the APIExports in the root
// workspace. An identity rotation flips that hash; the handler swaps the
// injected identity the moment the change arrives and expires the open
// watches sent with the old identity, so their informers relist with the
// new one instead of watching a storage prefix that is being drained.
// Exports of other workspaces, exports without an identity yet and deleted
// exports are ignored: an identity in use is only ever replaced by a new one.
//
// The handler works with any informer that sees the root workspace's
// APIExports: one against the cache server, or a scoped one on the root
// workspace. Feed it from one source only: a replicated copy lags behind a
// local one, and a handler fed from both briefly falls back to the old
// identity when the stale copy arrives.
func (ids *Identities) APIExportHandler() cache.ResourceEventHandler {
	exportNames := sets.New(ids.ExportNames()...)
	update := func(obj interface{}) {
		export, ok := obj.(*apisv1alpha2.APIExport)
		if !ok {
			return
		}
		if cluster := logicalcluster.From(export); !cluster.Empty() && cluster != core.RootCluster {
			return
		}
		if !exportNames.Has(export.Name) || export.Status.IdentityHash == "" {
			return
		}
		ids.set(export.Name, export.Status.IdentityHash)
	}
	return cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { update(obj) },
		UpdateFunc: func(_, obj interface{}) { update(obj) },
	}
}

// WrapRoundTripper returns a wrapper that injects the resolved identities
// into wildcard request URLs.
func (ids *Identities) WrapRoundTripper() func(rt http.RoundTripper) http.RoundTripper {
	return injectKcpIdentities(ids)
}

// NewConfigWithWildcardIdentities creates a rest.Config with injected resource identities for
// individual group or group resources. Each group or resource is coming from one APIExport whose
// names are passed in as a map.
//
// The returned resolve function asks the provider for the identities. It
// might return an error if an identity is not (yet) known. Only after it
// succeeds a client using the returned rest.Config can use the group and
// group resources with identities. The returned Identities give access to
// what was resolved and where it came from, and their APIExportHandler keeps
// them up to date, e.g. across an APIExport identity rotation.
func NewConfigWithWildcardIdentities(config *rest.Config,
	groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	provider Provider) (identityConfig *rest.Config, resolve func(ctx context.Context) error, ids *Identities) {
	identityRoundTripper, identityResolver, ids := NewWildcardIdentitiesWrappingRoundTripper(groupExportNames, groupResourceExportNames, provider)
	identityConfig = rest.CopyConfig(config)
	identityConfig.Wrap(identityRoundTripper)
	return identityConfig, identityResolver, ids
}

// NewWildcardIdentitiesWrappingRoundTripper creates an HTTP RoundTripper
// that injects resource identities for individual groups or group resources.
// Each group or resource is coming from one APIExport whose names are passed in as a map.
// The RoundTripper is exposed as a function that allows wrapping the RoundTripper
//
// The method also returns the resolve function that asks the provider for
// the identities. It might return an error if an identity is not (yet)
// known. Only after it succeeds a client using the returned RoundTripper can
// use the group and group resources with identities.
func NewWildcardIdentitiesWrappingRoundTripper(groupExportNames map[string]string,
	groupResourceExportNames map[schema.GroupResource]string,
	provider Provider) (func(rt http.RoundTripper) http.RoundTripper, func(ctx context.Context) error, *Identities) {
	ids := NewIdentities(groupExportNames, groupResourceExportNames)
	wrap := ids.WrapRoundTripper()
	resolve := func(ctx context.Context) error {
		return ids.Resolve(ctx, provider)
	}
	return wrap, resolve, ids
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
func injectKcpIdentities(ids *Identities) func(rt http.RoundTripper) http.RoundTripper {
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
				if _, current, err := decorateWildcardPathsWithResourceIdentities(origReq.URL.Path, ids); err != nil || current != identity {
					body.expire()
				}
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
func decorateWildcardPathsWithResourceIdentities(urlPath string, ids *Identities) (string, string, error) {
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
