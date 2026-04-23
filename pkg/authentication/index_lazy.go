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
	idx.authenticators.Start()
	return idx
}

func (idx *lazyIndex) Lookup(wsType logicalcluster.Path) (authenticator.Request, bool) {
	clusterPath, wstName := wsType.Split()
	if clusterPath.Empty() {
		return nil, false
	}

	state, err := idx.authenticators.Get(wsType.String(), func() (authenticatorState, error) {
		return idx.buildUnionAuthenticator(clusterPath, wstName)
	})
	if err != nil {
		if errors.Is(err, errCauseEmpty) {
			return nil, false
		}
		logger := klog.Background().WithValues("controller", controllerName, "workspaceType", wsType)
		logger.Error(err, "Failed to start workspace authenticator.")
		return nil, false
	}

	return state.authenticator, true
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
//  3. Caching WACs in the cache server: Also leads to more single point
//     of failure and pushes even more resources into the cache server.
//
// The only option I could somewhat see is an informer that can watch
// and update only requested resources. Then a shard could simply setup
// the pull-first informer per shard and build authenticators on demand.
// Then the watches for the WACs are set up and when these change the
// authenticator could be invalidated and rebuild.
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
		authenticators = append(authenticators, state.authenticator)
	}

	return authenticatorState{
		cancel:        parentCancel,
		authenticator: wrapWithSecurityFilters(authenticatorunion.New(authenticators...)),
	}, nil
}
