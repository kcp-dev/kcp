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
	"net/http"
	"sync"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/klog/v2"
	kubeauthenticator "k8s.io/kubernetes/pkg/kubeapiserver/authenticator"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
	proxyindex "github.com/kcp-dev/kcp/pkg/proxy/index"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

// AuthenticatorIndex implements a mapping from workspace type to authenticator.Request.
type AuthenticatorIndex interface {
	Lookup(wsType index.WorkspaceType) (authenticator.Request, bool)
}

// state keeps track of authenticators for each workspace type.
type state struct {
	lock                        sync.RWMutex
	clusterIndex                proxyindex.Index
	lifecycleCtx                context.Context
	workspaceTypeAuthenticators map[index.WorkspaceType][]authenticatorKey
	authConfigAuthenticators    map[authenticatorKey]authenticatorState
}

type authenticatorKey struct {
	cluster logicalcluster.Name
	name    string
}

type authenticatorState struct {
	cancel        context.CancelCauseFunc
	authenticator authenticator.Request
}

func newState(lifecycleCtx context.Context, clusterIndex proxyindex.Index) *state {
	return &state{
		clusterIndex:                clusterIndex,
		lifecycleCtx:                lifecycleCtx,
		workspaceTypeAuthenticators: map[index.WorkspaceType][]authenticatorKey{},
		authConfigAuthenticators:    map[authenticatorKey]authenticatorState{},
	}
}

func (c *state) UpsertWorkspaceType(wst *tenancyv1alpha1.WorkspaceType) {
	c.lock.Lock()
	defer c.lock.Unlock()

	clusterName := logicalcluster.From(wst)

	// convert auth config references to resolved (cluster,name) combinations
	authenticators := []authenticatorKey{}
	for _, authConfig := range wst.Spec.AuthenticationConfigurations {
		authConfigCluster, found := c.resolvePath(wst, authConfig.Path)
		if found {
			authenticators = append(authenticators, authenticatorKey{
				cluster: authConfigCluster,
				name:    authConfig.Configuration,
			})
		}
	}

	mapKey := index.WorkspaceType{
		Cluster: clusterName,
		Name:    wst.Name,
	}
	c.workspaceTypeAuthenticators[mapKey] = authenticators
}

func (c *state) resolvePath(parent logicalcluster.Object, path string) (logicalcluster.Name, bool) {
	if path == "" {
		return logicalcluster.From(parent), true
	}

	result, found := c.clusterIndex.LookupURL(logicalcluster.NewPath(path))
	if found {
		return result.Cluster, true
	}

	return "", false
}

func (c *state) DeleteWorkspaceType(wst *tenancyv1alpha1.WorkspaceType) {
	clusterName := logicalcluster.From(wst)
	mapKey := index.WorkspaceType{
		Cluster: clusterName,
		Name:    wst.Name,
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	delete(c.workspaceTypeAuthenticators, mapKey)
}

var (
	upsertCauseErr = errors.New("authentication configuration has changed")
	emptyCauseErr  = errors.New("no valid authentication methods configured")
)

func (c *state) UpsertWorkspaceAuthenticationConfiguration(authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	clusterName := logicalcluster.From(authConfig)
	mapKey := authenticatorKey{
		cluster: clusterName,
		name:    authConfig.Name,
	}

	// Stop and delete the old authenticator; do not lock for the entire duration of this function
	// because initializing the authenticator later might be comparatively slow.
	c.lock.Lock()
	authenticator, ok := c.authConfigAuthenticators[mapKey]
	if ok {
		authenticator.cancel(upsertCauseErr)
		delete(c.authConfigAuthenticators, mapKey)
	}
	c.lock.Unlock()

	// build new authenticator
	kubeAuthConfig := kubeauthenticator.Config{
		AuthenticationConfig: convertAuthenticationConfiguration(authConfig),
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

	c.authConfigAuthenticators[mapKey] = authenticatorState{
		cancel:        cancel,
		authenticator: authn,
	}
}

func (c *state) DeleteWorkspaceAuthenticationConfiguration(authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) {
	clusterName := logicalcluster.From(authConfig)
	mapKey := authenticatorKey{
		cluster: clusterName,
		name:    authConfig.Name,
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	// Stop and delete the old authenticator
	authenticator, ok := c.authConfigAuthenticators[mapKey]
	if ok {
		authenticator.cancel(upsertCauseErr)
		delete(c.authConfigAuthenticators, mapKey)
	}
}

func (c *state) Lookup(wsType index.WorkspaceType) (authenticator.Request, bool) {
	authenticatorKeys, ok := c.workspaceTypeAuthenticators[wsType]
	if !ok {
		return nil, false
	}

	var authenticators authenticatorUnion
	for _, key := range authenticatorKeys {
		authenticator, ok := c.authConfigAuthenticators[key]
		if ok {
			authenticators = append(authenticators, authenticator.authenticator)
		}
	}

	if len(authenticators) == 0 {
		return nil, false
	}

	return authenticators, true
}

type authenticatorUnion []authenticator.Request

var _ authenticator.Request = authenticatorUnion{}

func (u authenticatorUnion) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	for _, authenticator := range u {
		response, authenticated, err := authenticator.AuthenticateRequest(req)
		if err != nil || authenticated {
			return response, authenticated, err
		}
	}

	return nil, false, nil
}
