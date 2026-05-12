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
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/klog/v2"
	kubeauthenticator "k8s.io/kubernetes/pkg/kubeapiserver/authenticator"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// authenticatorSetupTimeout is duration to wait for an authenticator to
// be initialized, means it finished the discovery of its issuers and is
// ready to validate requests.
//
// 10s is the time upstream waits in the updateAuthenticationConfig when
// an authenticator is updated.
const authenticatorSetupTimeout = 10 * time.Second

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
	errCauseEvict       = errors.New("cache entry evicted")
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
	logger := klog.FromContext(ctx).WithValues("controller", controllerName)

	authn, _, _, _, err := kubeAuthConfig.New(ctx)
	if err != nil {
		logger.Error(err, "Failed to start workspace authenticator.")
		cancel(fmt.Errorf("authenticator failed to start: %w", err))
		return authenticatorState{}, err
	}

	// nil is returned whenever no valid individual auth method is configured.
	if authn == nil {
		cancel(errCauseEmpty)
		return authenticatorState{}, errCauseEmpty
	}

	if len(wac.Spec.JWT) > 0 {
		// There shouldn't be an authenticator without at least one JWT
		// at the very least because the spec validates this but better
		// safe than sorry.

		// Wait for the authenticator to be initialized.
		//
		// The authenticators do have a healthcheck, however this healthcheck is not exposed
		// and it is not easy to expose without intrusive changes.
		//
		// An alternative would've been to use the update function returned by kubeAuthConfig.New
		// which _does_ wait until the authenticator's healthcheck are ready, however that would
		// create each authenticator twice and just discard the first iteration.
		//
		// Instead the authenticator is validated by continually authenticating a dummy token
		// created with the first issuer until the error is no longer "not initialized".
		dummyToken := dummyTokenForIssuer(wac.Spec.JWT[0].Issuer.URL)
		if err := wait.PollUntilContextTimeout(ctx, time.Second, authenticatorSetupTimeout, true, func(ctx context.Context) (bool, error) {
			dummyReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)
			dummyReq.Header.Set("Authorization", "Bearer "+dummyToken)
			_, _, err := authn.AuthenticateRequest(dummyReq)
			return err == nil || !strings.Contains(err.Error(), "not initialized"), nil
		}); err != nil {
			logger.Error(err, "Failed to validate workspace authenticator.")
			cancel(fmt.Errorf("authenticator failed to validate within %q: %w", authenticatorSetupTimeout, err))
			return authenticatorState{}, err
		}
	}

	return authenticatorState{cancel: cancel, authenticator: authn}, nil
}

func dummyTokenForIssuer(issuer string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"iss":%q}`, issuer)))
	return header + "." + payload + "."
}

// wrapWithSecurityFilters wraps an authenticator with security filters
// that prevent workspace-local auth from escalating to system-level access.
func wrapWithSecurityFilters(authn authenticator.Request) authenticator.Request {
	authn = ForbidSystemUsernames(authn)
	groupFiltered := &GroupFilter{
		Authenticator:     authn,
		DropGroupPrefixes: []string{"system:"},
	}
	return &ExtraFilter{
		Authenticator:        groupFiltered,
		DropExtraKeyContains: []string{"kcp.io"},
	}
}
