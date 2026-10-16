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
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
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
		{Group: "core.kcp.io", Resource: "shards"}:                          "shards.core.kcp.io",
		{Group: "migration.kcp.io", Resource: "logicalclustermigrations"}:   "migration.kcp.io",
		{Group: "migration.kcp.io", Resource: "apiexportidentityrotations"}: "migration.kcp.io",
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
// Resolve and injected into wildcard request URLs by the round tripper. An
// identity, once resolved, never changes for the lifetime of the process.
type Identities struct {
	lock sync.RWMutex

	groupExportNames         map[string]string
	groupResourceExportNames map[schema.GroupResource]string

	groupIdentities         map[string]string
	groupResourceIdentities map[schema.GroupResource]string
	byExport                map[string]ResolvedIdentity

	classifier Classifier
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
// what was resolved and where it came from.
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
				urlPath, err := decorateWildcardPathsWithResourceIdentities(origReq.URL.Path, ids)
				if err != nil {
					return nil, err
				}
				if urlPath == origReq.URL.Path {
					return rt.RoundTrip(origReq)
				}

				req := *origReq // shallow copy
				req.URL = &url.URL{}
				*req.URL = *origReq.URL
				req.URL.Path = urlPath

				return rt.RoundTrip(&req)
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
func decorateWildcardPathsWithResourceIdentities(urlPath string, ids *Identities) (string, error) {
	// Check for: /clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces/root
	if !strings.HasPrefix(urlPath, "/clusters/*/apis/") {
		return urlPath, nil
	}

	comps := strings.SplitN(strings.TrimPrefix(urlPath, "/"), "/", 7)
	if len(comps) < 6 {
		return urlPath, nil
	}

	// It's possible the outgoing request already has an identity specified. Make sure we exclude that when
	// determining the resource in question.
	parts := strings.SplitN(comps[5], ":", 2)
	if len(parts) == 0 {
		return "", fmt.Errorf("invalid resource %q", comps[5])
	}

	resource := parts[0]

	gr := schema.GroupResource{Group: comps[3], Resource: resource}
	if id, found := ids.grIdentity(gr); found && gr != corev1alpha1.Resource("logicalclusters") {
		if len(id) == 0 {
			return "", fmt.Errorf("identity for %s is unknown", gr)
		}

		passedInIdentity := ""
		if len(parts) > 1 {
			passedInIdentity = parts[1]
		}

		if passedInIdentity != "" && passedInIdentity != id {
			return "", fmt.Errorf("invalid identity %q for resource %q", passedInIdentity, resource)
		}

		comps[5] = resource + ":" + id

		return "/" + path.Join(comps...), nil
	}

	return urlPath, nil
}
