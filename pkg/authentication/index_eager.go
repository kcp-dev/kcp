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
	"sync"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"

	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// eagerIndex keeps track of authenticators for each workspace type.
type eagerIndex struct {
	lock sync.RWMutex
	// This component's job is to hand the application's long-lived context to new,
	// ad-hoc started goroutines, so it must store the context here. The context
	// available for a reconciliation will be cancelled too early and is not suitable
	// for authenticators.
	//nolint:containedctx
	lifecycleCtx                context.Context
	baseAudiences               authenticator.Audiences
	workspaceTypeAuthenticators map[string]map[logicalcluster.Path][]authenticatorKey
	authConfigAuthenticators    map[string]map[authenticatorKey]authenticatorState
}

func NewIndex(lifecycleCtx context.Context, baseAudiences authenticator.Audiences) *eagerIndex {
	return &eagerIndex{
		lifecycleCtx:                lifecycleCtx,
		workspaceTypeAuthenticators: map[string]map[logicalcluster.Path][]authenticatorKey{},
		authConfigAuthenticators:    map[string]map[authenticatorKey]authenticatorState{},
		baseAudiences:               baseAudiences,
	}
}

func (i *eagerIndex) UpsertWorkspaceType(shard string, wst *tenancyv1alpha1.WorkspaceType) {
	i.lock.Lock()
	defer i.lock.Unlock()

	clusterName := logicalcluster.From(wst)

	authenticators := make([]authenticatorKey, 0, len(wst.Spec.AuthenticationConfigurations))
	for _, authConfig := range wst.Spec.AuthenticationConfigurations {
		authenticators = append(authenticators, authenticatorKey{
			cluster: clusterName,
			name:    authConfig.Name,
		})
	}

	wstKey := getWorkspaceTypeKey(wst)

	if i.workspaceTypeAuthenticators[shard] == nil {
		i.workspaceTypeAuthenticators[shard] = map[logicalcluster.Path][]authenticatorKey{}
	}
	i.workspaceTypeAuthenticators[shard][wstKey] = authenticators
}

func (i *eagerIndex) DeleteWorkspaceType(shard string, wst *tenancyv1alpha1.WorkspaceType) {
	wstKey := getWorkspaceTypeKey(wst)

	i.lock.Lock()
	defer i.lock.Unlock()

	delete(i.workspaceTypeAuthenticators[shard], wstKey)
	if len(i.workspaceTypeAuthenticators[shard]) == 0 {
		delete(i.workspaceTypeAuthenticators, shard)
	}
}

func (i *eagerIndex) UpsertWorkspaceAuthenticationConfiguration(shard string, authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	mapKey := getAuthConfigKey(authConfig)

	// Stop and delete the old authenticator; do not lock for the entire duration of this function
	// because initializing the authenticator later might be comparatively slow.
	i.lock.Lock()
	i.stopAuthenticator(shard, mapKey, errCauseUpsert)
	i.lock.Unlock()

	state, err := buildAuthenticator(i.lifecycleCtx, i.baseAudiences, authConfig)
	if err != nil {
		// TODO(ntnn): If an authenticator fails to build this will just
		// quietly go stale with no reattempts. Arguably - if an
		// authenticator fails to build off of a WAC it's likely
		// a configuration issue.
		return
	}

	i.lock.Lock()
	defer i.lock.Unlock()

	if i.authConfigAuthenticators[shard] == nil {
		i.authConfigAuthenticators[shard] = map[authenticatorKey]authenticatorState{}
	}
	i.authConfigAuthenticators[shard][mapKey] = state
}

func (i *eagerIndex) stopAuthenticator(shard string, key authenticatorKey, cause error) {
	authenticator, ok := i.authConfigAuthenticators[shard][key]
	if ok {
		authenticator.cancel(cause)
		delete(i.authConfigAuthenticators[shard], key)
		if len(i.authConfigAuthenticators[shard]) == 0 {
			delete(i.authConfigAuthenticators, shard)
		}
	}
}

func (i *eagerIndex) DeleteWorkspaceAuthenticationConfiguration(shard string, authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	mapKey := getAuthConfigKey(authConfig)

	// Stop and delete the old authenticator
	i.lock.Lock()
	defer i.lock.Unlock()

	i.stopAuthenticator(shard, mapKey, errCauseDelete)
}

func (i *eagerIndex) DeleteShard(shardName string) {
	i.lock.Lock()
	defer i.lock.Unlock()

	// stop all authenticators on this shard
	for key := range i.authConfigAuthenticators[shardName] {
		i.stopAuthenticator(shardName, key, errCauseDeleteShard)
	}

	delete(i.workspaceTypeAuthenticators, shardName)
	delete(i.authConfigAuthenticators, shardName)
}

func (i *eagerIndex) Lookup(wsType logicalcluster.Path) (authenticator.Request, bool) {
	var (
		shard             string
		authenticatorKeys []authenticatorKey
	)

	i.lock.RLock()
	defer i.lock.RUnlock()

	for shardKey, authenticatorsMap := range i.workspaceTypeAuthenticators {
		var found bool
		authenticatorKeys, found = authenticatorsMap[wsType]
		if found {
			shard = shardKey
			break
		}
	}

	if len(authenticatorKeys) == 0 {
		return nil, false
	}

	var authenticators []authenticator.Request
	for _, key := range authenticatorKeys {
		authenticator, ok := i.authConfigAuthenticators[shard][key]
		if ok {
			authenticators = append(authenticators, authenticator.authenticator)
		}
	}

	if len(authenticators) == 0 {
		return nil, false
	}

	authenticator := authenticatorunion.New(authenticators...)

	// ensure that per-workspace auth cannot be used to become a system: user/group
	authenticator = ForbidSystemUsernames(authenticator)
	groupFiltered := &GroupFilter{
		Authenticator:     authenticator,
		DropGroupPrefixes: []string{"system:"},
	}
	extraFiltered := &ExtraFilter{
		Authenticator:        groupFiltered,
		DropExtraKeyContains: []string{"kcp.io"},
	}

	return extraFiltered, true
}
