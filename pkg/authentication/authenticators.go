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
	"fmt"
	"net/http"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/group"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

func NewStandaloneWorkspaceAuthenticator(clusterIndex *index.State, authIndex AuthenticatorIndex) authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		clusterName, err := request.ClusterNameFrom(req.Context())
		if err != nil {
			klog.FromContext(req.Context()).V(4).Info("standalone workspace authenticator skipped: request has no cluster context", "path", req.URL.Path, "err", err)
			return nil, false, nil
		}

		result, found := clusterIndex.Lookup(clusterName.Path())
		if !found {
			return nil, false, nil
		}

		// workspacemounts have no type
		if result.Type.Empty() {
			return nil, false, nil
		}

		reqAuthenticator, ok := authIndex.Lookup(result.Type)
		if !ok {
			return nil, false, nil
		}

		reqAuthenticator = group.NewAuthenticatedGroupAdder(reqAuthenticator)

		// make the authenticator always add the target cluster to the user scopes
		reqAuthenticator = withClusterScope(reqAuthenticator)

		return reqAuthenticator.AuthenticateRequest(req)
	})
}

func NewWorkspaceAuthenticator() authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		reqAuthenticator, ok := WorkspaceAuthenticatorFrom(req.Context())
		if !ok {
			return nil, false, nil
		}

		return reqAuthenticator.AuthenticateRequest(req)
	})
}

func withClusterScope(delegate authenticator.Request) authenticator.Request {
	return authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
		response, authenticated, ok := delegate.AuthenticateRequest(req)
		if !authenticated {
			return response, authenticated, ok
		}

		// withClusterScope is used both in the front-proxy and shards.
		// Both set the cluster name on different context keys due to
		// differing handler chains and middlewares.
		//
		// On top of that, synthetic requests like a TokenReview may
		// pass through the authentication chain with the cluster only on
		// the context key from the request package, not the lookup
		// package.
		//
		// To handle both front-proxy and shards both .ClusterNameFrom
		// functions are checked.
		cluster := lookup.ClusterNameFrom(req.Context())
		if cluster.Empty() {
			var err error
			cluster, err = request.ClusterNameFrom(req.Context())
			if err != nil {
				klog.FromContext(req.Context()).Error(err, "withClusterScope: no cluster name found in lookup or request context; skipping scope extras")
				return response, authenticated, ok
			}
		}

		extra := response.User.GetExtra()
		if extra == nil {
			extra = map[string][]string{}
		}
		extra["authentication.kcp.io/scopes"] = append(extra["authentication.kcp.io/scopes"], fmt.Sprintf("cluster:%s", cluster))
		extra["authentication.kcp.io/cluster-name"] = []string{cluster.String()}

		response.User = &user.DefaultInfo{
			Name:   response.User.GetName(),
			UID:    response.User.GetUID(),
			Groups: response.User.GetGroups(),
			Extra:  extra,
		}

		return response, authenticated, ok
	})
}
