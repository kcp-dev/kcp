/*
Copyright 2025 The KCP Authors.

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
	"sync"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	authenticatorunion "k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/klog/v2"
	kubeauthenticator "k8s.io/kubernetes/pkg/kubeapiserver/authenticator"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// AuthenticatorIndex implements a mapping from workspace type to authenticator.Request.
type AuthenticatorIndex interface {
	Lookup(wsType logicalcluster.Path) (authenticator.Request, bool)
}

// state keeps track of authenticators for each workspace type.
type state struct {
	lock                        sync.RWMutex
	lifecycleCtx                context.Context
	baseAudiences               authenticator.Audiences
	workspaceTypeAuthenticators map[string]map[logicalcluster.Path][]authenticatorKey
	authConfigAuthenticators    map[string]map[authenticatorKey]authenticatorState
}

type authenticatorKey struct {
	cluster logicalcluster.Name
	name    string
}

type authenticatorState struct {
	cancel        context.CancelCauseFunc
	authenticator authenticator.Request
}

func NewIndex(lifecycleCtx context.Context, baseAudiences authenticator.Audiences) *state {
	return &state{
		lifecycleCtx:                lifecycleCtx,
		workspaceTypeAuthenticators: map[string]map[logicalcluster.Path][]authenticatorKey{},
		authConfigAuthenticators:    map[string]map[authenticatorKey]authenticatorState{},
		baseAudiences:               baseAudiences,
	}
}

func (c *state) UpsertWorkspaceType(shard string, wst *tenancyv1alpha1.WorkspaceType) {
	c.lock.Lock()
	defer c.lock.Unlock()

	clusterName := logicalcluster.From(wst)

	authenticators := []authenticatorKey{}
	for _, authConfig := range wst.Spec.AuthenticationConfigurations {
		authenticators = append(authenticators, authenticatorKey{
			cluster: clusterName,
			name:    authConfig.Configuration,
		})
	}

	wstKey := getWorkspaceTypeKey(wst)

	if c.workspaceTypeAuthenticators[shard] == nil {
		c.workspaceTypeAuthenticators[shard] = map[logicalcluster.Path][]authenticatorKey{}
	}
	c.workspaceTypeAuthenticators[shard][wstKey] = authenticators
}

func getWorkspaceTypeKey(wst *tenancyv1alpha1.WorkspaceType) logicalcluster.Path {
	return logicalcluster.NewPath(wst.Annotations[core.LogicalClusterPathAnnotationKey]).Join(wst.Name)
}

func (c *state) DeleteWorkspaceType(shard string, wst *tenancyv1alpha1.WorkspaceType) {
	wstKey := getWorkspaceTypeKey(wst)

	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.workspaceTypeAuthenticators[shard], wstKey)
	if len(c.workspaceTypeAuthenticators[shard]) == 0 {
		delete(c.workspaceTypeAuthenticators, shard)
	}
}

var (
	upsertCauseErr      = errors.New("authentication configuration has changed")
	deleteCauseErr      = errors.New("authentication configuration has been deleted")
	deleteShardCauseErr = errors.New("shard has been deleted")
	emptyCauseErr       = errors.New("no valid authentication methods configured")
)

func (c *state) UpsertWorkspaceAuthenticationConfiguration(shard string, authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	mapKey := getAuthConfigKey(authConfig)

	// Stop and delete the old authenticator; do not lock for the entire duration of this function
	// because initializing the authenticator later might be comparatively slow.
	c.lock.Lock()
	c.stopAuthenticator(shard, mapKey, upsertCauseErr)
	c.lock.Unlock()

	// build new authenticator
	kubeAuthConfig := kubeauthenticator.Config{
		AuthenticationConfig: convertAuthenticationConfiguration(authConfig),
		APIAudiences:         c.baseAudiences,
	}

	ctx, cancel := context.WithCancelCause(c.lifecycleCtx)

	authn, _, _, _, err := kubeAuthConfig.New(ctx)
	if err != nil {
		logger := klog.FromContext(ctx).WithValues("controller", controllerName)
		logger.Error(err, "Failed to start workspace authenticator.")

		cancel(fmt.Errorf("authenticator failed to start: %w", err))
		return
	}

	// nil is returned whenever no valid individual auth method is configured.
	if authn == nil {
		cancel(emptyCauseErr)
		return
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	if c.authConfigAuthenticators[shard] == nil {
		c.authConfigAuthenticators[shard] = map[authenticatorKey]authenticatorState{}
	}
	c.authConfigAuthenticators[shard][mapKey] = authenticatorState{
		cancel:        cancel,
		authenticator: authn,
	}
}

func getAuthConfigKey(authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) authenticatorKey {
	return authenticatorKey{
		cluster: logicalcluster.From(authConfig),
		name:    authConfig.Name,
	}
}

func (c *state) stopAuthenticator(shard string, key authenticatorKey, cause error) {
	authenticator, ok := c.authConfigAuthenticators[shard][key]
	if ok {
		authenticator.cancel(cause)
		delete(c.authConfigAuthenticators[shard], key)
		if len(c.authConfigAuthenticators[shard]) == 0 {
			delete(c.authConfigAuthenticators, shard)
		}
	}
}

func (c *state) DeleteWorkspaceAuthenticationConfiguration(shard string, authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	mapKey := getAuthConfigKey(authConfig)

	// Stop and delete the old authenticator
	c.lock.Lock()
	defer c.lock.Unlock()

	c.stopAuthenticator(shard, mapKey, deleteCauseErr)
}

func (c *state) DeleteShard(shardName string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// stop all authenticators on this shard
	for key := range c.authConfigAuthenticators[shardName] {
		c.stopAuthenticator(shardName, key, deleteShardCauseErr)
	}

	delete(c.workspaceTypeAuthenticators, shardName)
	delete(c.authConfigAuthenticators, shardName)
}

func (c *state) Lookup(wsType logicalcluster.Path) (authenticator.Request, bool) {
	var (
		shard             string
		authenticatorKeys []authenticatorKey
	)

	for shardKey, authenticatorsMap := range c.workspaceTypeAuthenticators {
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
		authenticator, ok := c.authConfigAuthenticators[shard][key]
		if ok {
			authenticators = append(authenticators, authenticator.authenticator)
		}
	}

	if len(authenticators) == 0 {
		return nil, false
	}

	return authenticatorunion.New(authenticators...), true
}
