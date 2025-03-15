/*
Copyright 2021 The KCP Authors.

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

package framework

import (
	"context"
	"math/rand"
	"strings"
	"testing"

	"github.com/martinlindhe/base36"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/admission/workspace"
	"github.com/kcp-dev/kcp/pkg/authorization"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/server"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	testing2 "github.com/kcp-dev/kcp/sdk/testing"
)

type ArtifactFunc func(*testing.T, func() (runtime.Object, error))

// StaticTokenUserConfig returns a user config based on static user tokens defined in "test/e2e/framework/auth-tokens.csv".
// The token being used is "[username]-token".
func StaticTokenUserConfig(username string, cfg *rest.Config) *rest.Config {
	return ConfigWithToken(username+"-token", cfg)
}

// ConfigWithToken returns a copy of the given rest.Config with the given token set.
func ConfigWithToken(token string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = nil
	cfgCopy.KeyData = nil
	cfgCopy.BearerToken = token
	return cfgCopy
}

// UniqueGroup returns a unique API group with the given suffix by prefixing
// some random 8 character base36 string. suffix must start with a dot if the
// random string should be dot-separated.
func UniqueGroup(suffix string) string {
	ret := strings.ToLower(base36.Encode(rand.Uint64())[:8]) + suffix
	if ret[0] >= '0' && ret[0] <= '9' {
		return "a" + ret[1:]
	}
	return ret
}

// VirtualWorkspaceURL returns the virtual workspace URL base URL of the shard
// the workspace is scheduled on.
func VirtualWorkspaceURL(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, ws *tenancyv1alpha1.Workspace, urls []string) (string, bool, error) {
	shard, err := testing2.WorkspaceShard(ctx, kcpClusterClient, ws)
	if err != nil {
		return "", false, err
	}

	for _, url := range urls {
		if strings.HasPrefix(url, shard.Spec.VirtualWorkspaceURL) {
			return url, true, nil
		}
	}

	return "", false, nil
}

// ExportVirtualWorkspaceURLs returns the URLs of the virtual workspaces of the
// given APIExport.
func ExportVirtualWorkspaceURLs(export *apisv1alpha1.APIExport) []string {
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	urls := make([]string, 0, len(export.Status.VirtualWorkspaces))
	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	for _, vw := range export.Status.VirtualWorkspaces {
		urls = append(urls, vw.URL)
	}
	return urls
}

// WithRequiredGroups is a privileged action, so we return a privileged option type, and only the helpers that
// use the system:master config can consume this. However, workspace initialization requires a valid user annotation
// on the workspace object to impersonate during initialization, and system:master bypasses setting that, so we
// end up needing to hard-code something conceivable.
func WithRequiredGroups(groups ...string) testing2.PrivilegedWorkspaceOption {
	return func(ws *tenancyv1alpha1.Workspace) {
		if ws.Annotations == nil {
			ws.Annotations = map[string]string{}
		}
		ws.Annotations[authorization.RequiredGroupsAnnotationKey] = strings.Join(groups, ",")
		userInfo, err := workspace.WorkspaceOwnerAnnotationValue(&user.DefaultInfo{
			Name:   server.KcpBootstrapperUserName,
			Groups: []string{user.AllAuthenticated, bootstrappolicy.SystemKcpWorkspaceBootstrapper},
		})
		if err != nil {
			// should never happen
			panic(err)
		}
		ws.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = userInfo
	}
}
