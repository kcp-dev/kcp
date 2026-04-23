/*
Copyright 2025 The kcp Authors.

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

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/klog/v2"
	kubeauthenticator "k8s.io/kubernetes/pkg/kubeapiserver/authenticator"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// AuthenticatorIndex implements a mapping from workspace type to authenticator.Request.
type AuthenticatorIndex interface {
	Lookup(wsType logicalcluster.Path) (authenticator.Request, bool)
}

type authenticatorKey struct {
	cluster logicalcluster.Name
	name    string
}

type authenticatorState struct {
	cancel        context.CancelCauseFunc
	authenticator authenticator.Request
}

func getWorkspaceTypeKey(wst *tenancyv1alpha1.WorkspaceType) logicalcluster.Path {
	return logicalcluster.NewPath(wst.Annotations[core.LogicalClusterPathAnnotationKey]).Join(wst.Name)
}

var (
	errCauseUpsert      = errors.New("authentication configuration has changed")
	errCauseDelete      = errors.New("authentication configuration has been deleted")
	errCauseDeleteShard = errors.New("shard has been deleted")
	errCauseEmpty       = errors.New("no valid authentication methods configured")
)

func getAuthConfigKey(authConfig *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) authenticatorKey {
	return authenticatorKey{
		cluster: logicalcluster.From(authConfig),
		name:    authConfig.Name,
	}
}

// buildAuthenticator builds a JWT authenticator from the provided WAC.
func buildAuthenticator(
	lifecycleCtx context.Context,
	baseAudiences authenticator.Audiences,
	wac *tenancyv1alpha1.WorkspaceAuthenticationConfiguration,
) (authenticatorState, error) {
	kubeAuthConfig := kubeauthenticator.Config{
		AuthenticationConfig: convertAuthenticationConfiguration(wac),
		APIAudiences:         baseAudiences,
	}

	ctx, cancel := context.WithCancelCause(lifecycleCtx)

	authn, _, _, _, err := kubeAuthConfig.New(ctx)
	if err != nil {
		logger := klog.FromContext(ctx).WithValues("controller", controllerName)
		logger.Error(err, "Failed to start workspace authenticator.")

		cancel(fmt.Errorf("authenticator failed to start: %w", err))
		return authenticatorState{}, err
	}

	// nil is returned whenever no valid individual auth method is configured.
	if authn == nil {
		cancel(errCauseEmpty)
		return authenticatorState{}, errCauseEmpty
	}

	return authenticatorState{cancel: cancel, authenticator: authn}, nil
}
