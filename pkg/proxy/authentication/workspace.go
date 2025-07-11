/*
Copyright 2022 The KCP Authors.

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
	"fmt"
	"net/http"
	"time"

	"k8s.io/apiserver/pkg/apis/apiserver"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	kubeauthenticator "k8s.io/kubernetes/pkg/kubeapiserver/authenticator"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/pkg/proxy/index"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

type AuthProvider interface {
	GetAuthenticator(req *http.Request) (authenticator.Request, error)
}

type WorkspaceAuthProvider struct {
	appCtx context.Context
	index  index.Index
}

func NewWorkspaceAuthProvider(appCtx context.Context, index index.Index) *WorkspaceAuthProvider {
	return &WorkspaceAuthProvider{
		appCtx: appCtx,
		index:  index,
	}
}

func (p *WorkspaceAuthProvider) GetAuthenticator(req *http.Request) (authenticator.Request, error) {
	clusterName := lookup.ClusterNameFrom(req.Context())
	fmt.Printf("XRSTF: clusterName: %v\n", clusterName)

	shardURL := lookup.ShardURLFrom(req.Context())
	fmt.Printf("XRSTF: shardURL: %v\n", shardURL.String())

	if clusterName == "root" {
		return nil, nil
	}

	authConfig := kubeauthenticator.Config{
		AuthenticationConfig: &apiserver.AuthenticationConfiguration{
			JWT: []apiserver.JWTAuthenticator{{
				Issuer: apiserver.Issuer{
					URL:       "https://auth2.platform-dev.lab.kubermatic.io/dex",
					Audiences: []string{"kdp-kubelogin"},
				},
				ClaimMappings: apiserver.ClaimMappings{
					Username: apiserver.PrefixedClaimOrExpression{
						Claim:  "email",
						Prefix: ptr.To("oidc2:"),
					},
					Groups: apiserver.PrefixedClaimOrExpression{
						Claim:  "groups",
						Prefix: ptr.To("oidc2:"),
					},
				},
			}},
		},
	}

	authn, _, _, _, err := authConfig.New(p.appCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to construct authenticator: %w", err)
	}
	// give auther time for discovery
	time.Sleep(5 * time.Second)

	fmt.Printf("AUTHER: %#v\n", authn)

	return authn, nil
}

type WorkspaceAuthenticator struct {
	delegate authenticator.Request
	provider AuthProvider
}

func NewWorkspaceAuthenticator(delegate authenticator.Request, provider AuthProvider) *WorkspaceAuthenticator {
	return &WorkspaceAuthenticator{
		delegate: delegate,
		provider: provider,
	}
}

var _ authenticator.Request = &WorkspaceAuthenticator{}

func (a *WorkspaceAuthenticator) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	response, authenticated, err := a.delegate.AuthenticateRequest(req)
	if err != nil || authenticated {
		return response, authenticated, err
	}

	authenticator, err := a.provider.GetAuthenticator(req)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get workspace authentication: %w", err)
	}

	return authenticator.AuthenticateRequest(req)
}
