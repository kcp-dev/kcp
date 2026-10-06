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

package authentication

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/shardlookup"
)

// lazyIndex, like eagerIndex, keeps track of authenticators for
// workspace types - but only builds them on demand and caches them for
// a fixed duration.
//
// Authenticators are very expensive, so it is not reasonable to keep
// authenticators running on shards (that are very busy anyhow) for an
// indefinite period of time. At the same time shards must be able to
// handle per-workspace authentication and authorization to handle
// requests like TokenReviews and similar requests coming through
// virtual workspaces.
type lazyIndex struct {
	lifecycleCtx  context.Context //nolint:containedctx
	baseAudiences authenticator.Audiences

	localWSTIndexer cache.Indexer
	cacheWSTIndexer cache.Indexer

	getWAC func(clusterName logicalcluster.Name, name string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error)

	authenticators *shardlookup.TTLCache[authenticatorState]
}

// NewLazyIndex creates an AuthenticatorIndex that builds authenticators on demand and caches them with a TTL.
//
// localWSTIndexer and cacheWSTIndexer are informer indexers for
// WorkspaceType objects (local shard and cache server respectively).
//
// getWAC retrieves a WorkspaceAuthenticationConfiguration by logical
// cluster name and object name.
func NewLazyIndex(
	lifecycleCtx context.Context,
	baseAudiences authenticator.Audiences,
	localWSTIndexer, cacheWSTIndexer cache.Indexer,
	getWAC func(logicalcluster.Name, string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error),
) AuthenticatorIndex {
	idx := &lazyIndex{
		lifecycleCtx:    lifecycleCtx,
		baseAudiences:   baseAudiences,
		localWSTIndexer: localWSTIndexer,
		cacheWSTIndexer: cacheWSTIndexer,
		getWAC:          getWAC,
		authenticators: shardlookup.NewTTLCacheWithOptions[authenticatorState](shardlookup.TTLOptions{
			// TODO(ntnn): Make the TTLs configurable via flags? On busy
			// instances with established patterns it might be
			// preferable to increase the SuccessTTL to not waste cycles
			// rebuilding the same authenticators every 2h.
			SuccessTTL: 2 * time.Hour,
			FailureTTL: 10 * time.Second,
		}),
	}
	idx.authenticators.OnEviction(func(state authenticatorState) {
		if state.cancel != nil {
			state.cancel(errCauseEvict)
		}
	})
	idx.authenticators.StartWithContext(lifecycleCtx)
	return idx
}

// Lookup retrieves or builds the request WAC for the WorkspaceType.
//
// If the authenticator is built and cached it is checked against the
// currently known RVs of the WorkspaceType and
// WorkspaceAuthenticatorConfigurations.
// If the WST or the WAC resource version does not match the cached
// authenticators it is rebuild.
func (idx *lazyIndex) Lookup(wsType logicalcluster.Path) (authenticator.Request, bool) {
	clusterPath, wstName := wsType.Split()
	if clusterPath.Empty() {
		return nil, false
	}

	state, ok := idx.lookup(wsType, clusterPath, wstName)
	if !ok {
		return nil, false
	}

	// check if the WAC has been updated in the meantime
	if idx.isCurrent(clusterPath, wstName, state) {
		return state.authenticator, true
	}

	// delete and fetch again, the authenticator will be rebuilt
	idx.authenticators.Delete(wsType.String())
	state, ok = idx.lookup(wsType, clusterPath, wstName)
	return state.authenticator, ok
}

func (idx *lazyIndex) lookup(wsType, clusterPath logicalcluster.Path, wstName string) (authenticatorState, bool) {
	state, err := idx.authenticators.Get(wsType.String(), func() (authenticatorState, error) {
		return idx.buildUnionAuthenticator(clusterPath, wstName)
	})
	if err != nil {
		if errors.Is(err, errCauseEmpty) {
			return authenticatorState{}, false
		}
		logger := klog.Background().WithValues("controller", controllerName, "workspaceType", wsType)
		logger.Error(err, "Failed to start workspace authenticator.")
		return authenticatorState{}, false
	}
	return state, true
}

// isCurrent reports whether the WorkspaceType and WACs state was built from are unchanged.
func (idx *lazyIndex) isCurrent(clusterPath logicalcluster.Path, wstName string, state authenticatorState) bool {
	wst, err := indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](
		tenancyv1alpha1.Resource("workspacetypes"),
		idx.localWSTIndexer, idx.cacheWSTIndexer,
		clusterPath, wstName,
	)
	if err != nil || wst.ResourceVersion != state.wstResourceVersion {
		return false
	}

	clusterName := logicalcluster.From(wst)
	for name, resourceVersion := range state.wacResourceVersions {
		wac, err := idx.getWAC(clusterName, name)
		if err != nil || wac.ResourceVersion != resourceVersion {
			return false
		}
	}
	return true
}

// buildUnionAuthenticator builds new authenticators for each referenced
// WAC and then returns a union of all of them.
//
// The authenticators cannot be shared because if three WorkspaceTypes
// share _some_ WSTs but get cached on one shard at different times what
// could happen is that the first WorkspaceType builds authenticators
// for all WACs it uses.
// Then and hour later the second WST is cached and reuses some of the
// running authenticators from the WACs both WSTs use. Then the first
// WST gets evicted, cancels its authenticators the union
// authenticators of the second WST is borked.
//
// Caching the authenticators from the WACs in a similar way has the
// same problem and keeping the authenticators alive would either
// require tracking which authenticator is used where _and_ keeping them
// alive longer than the intended TTL.
//
// All things considered not a good or ideal solution, but considering
// the constraints this is ok for now.
//
// NOTE(ntnn): If this comes back with a vengeance other avenues can be
// explored. However the other avenues are all ~~terrible~~ less optimal:
//  1. Letting front-proxy handle TokenReview -> Doesn't fix requests
//     coming through virtual workspaces
//  2. Redirecting per-workspace auth entirely to front-proxy just loads
//     more things off to the front-proxy, leading to more single point
//     of failure
//
// TODO(ntnn): Since WACs are now pushed to the cache-server and we are
// already tracking the RVs in the cached authenticators this could be
// optimized.
// Handlers on the WAC could rebuild WAC authenticators and the union
// authenticators using them.
// For instances using a lot per-workspace authentication with the same
// WAC reused across them that would reduce resource consumption.
func (idx *lazyIndex) buildUnionAuthenticator(clusterPath logicalcluster.Path, wstName string) (authenticatorState, error) {
	wst, err := indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](
		tenancyv1alpha1.Resource("workspacetypes"),
		idx.localWSTIndexer, idx.cacheWSTIndexer,
		clusterPath, wstName,
	)
	if err != nil {
		return authenticatorState{}, err
	}
	if len(wst.Spec.AuthenticationConfigurations) == 0 {
		return authenticatorState{}, errCauseEmpty
	}

	parentCtx, parentCancel := context.WithCancelCause(idx.lifecycleCtx)

	clusterName := logicalcluster.From(wst)
	var authenticators []authenticator.Request
	wacResourceVersions := make(map[string]string, len(wst.Spec.AuthenticationConfigurations))

	for _, ref := range wst.Spec.AuthenticationConfigurations {
		wac, err := idx.getWAC(clusterName, ref.Name)
		if err != nil {
			err = fmt.Errorf("error getting WorkspaceAuthenticationConfiguration %q from %q: %w", ref.Name, clusterName, err)
			parentCancel(err)
			return authenticatorState{}, err
		}

		state, err := buildAuthenticator(parentCtx, idx.baseAudiences, wac)
		if err != nil {
			err = fmt.Errorf("error building authenticator for WorkspaceAuthenticationConfiguration %q from %q: %w", ref.Name, clusterName, err)
			parentCancel(err)
			return authenticatorState{}, err
		}
		if err := waitForAuthenticatorInit(parentCtx, state.authenticator, wac); err != nil {
			err = fmt.Errorf("authenticator for WorkspaceAuthenticationConfiguration %q from %q failed to validate within %q: %w", ref.Name, clusterName, authenticatorSetupTimeout, err)
			parentCancel(err)
			return authenticatorState{}, err
		}
		authenticators = append(authenticators, state.authenticator)
		wacResourceVersions[wac.Name] = wac.ResourceVersion
	}

	return authenticatorState{
		cancel:              parentCancel,
		authenticator:       wrapWithSecurityFilters(authenticatorunion.New(authenticators...)),
		wstResourceVersion:  wst.ResourceVersion,
		wacResourceVersions: wacResourceVersions,
	}, nil
}
