/*
Copyright 2022 The kcp Authors.

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

package plugin

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpfakeclient "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
)

func TestUse(t *testing.T) {
	homeWorkspace := logicalcluster.NewPath("root:users:ab:cd:user-name")

	discoveryFor := func(pths ...string) map[logicalcluster.Path][]*metav1.APIResourceList {
		ret := make(map[logicalcluster.Path][]*metav1.APIResourceList, len(pths))
		for _, pth := range pths {
			ret[logicalcluster.NewPath(pth)] = []*metav1.APIResourceList{
				{GroupVersion: "tenancy.kcp.io/v1alpha1", APIResources: []metav1.APIResource{{Name: "workspaces"}}},
			}
		}
		return ret
	}

	tests := []struct {
		name   string
		config clientcmdapi.Config

		existingWorkspaces map[logicalcluster.Name][]string
		getWorkspaceErrors map[logicalcluster.Path]error
		discovery          map[logicalcluster.Path][]*metav1.APIResourceList
		discoveryErrors    map[logicalcluster.Path]error
		unready            map[logicalcluster.Path]map[string]bool // unready workspaces
		apiBindings        []apisv1alpha2.APIBinding               // APIBindings that exist in the destination workspace, if any
		destination        string                                  // workspace set to 'current' at the end of execution
		short              bool

		param string

		expected   *clientcmdapi.Config
		wantStdout []string
		wantStderr []string
		wantErrors []string
		wantErr    bool
		noWarn     bool
	}{
		{
			name:   "current, context named workspace",
			config: *NewKubeconfig().WithKcpCurrent("root:foo:bar").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       ".",
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name: "current, no cluster URL",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts:  map[string]*clientcmdapi.Context{"workspace.kcp.io/current": {Cluster: "workspace.kcp.io/current", AuthInfo: "test"}},
				Clusters:  map[string]*clientcmdapi.Cluster{"workspace.kcp.io/current": {Server: "https://test"}},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
			},
			param:      ".",
			wantStdout: []string{"Current workspace is the URL \"https://test\""},
		},
		{
			name:   "workspace name",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, short output",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			short:       true,
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			wantStdout:  []string{"root:foo:bar"},
		},
		{
			name:    "workspace name, no cluster URL",
			config:  *NewKubeconfig().WithKcpCurrent("").Build(),
			param:   "bar",
			wantErr: true,
		},
		{
			name:   "absolute name with access to parent",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "root:foo:bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:               "absolute name without access to parent",
			config:             *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discovery:          discoveryFor("root:foo:bar"),
			param:              "root:foo:bar",
			expected:           NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination:        "root:foo:bar",
			wantStdout:         []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:               "absolute workspace doesn't exist error",
			config:             *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewNotFound(schema.GroupResource{}, "bar")},
			discoveryErrors: map[logicalcluster.Path]error{
				logicalcluster.NewPath("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
			},
			param:      "root:foo:foe",
			wantErr:    true,
			wantErrors: []string{"workspace \"root:foo:foe\" not found"},
		},
		{
			name:               "absolute workspace access not permitted",
			config:             *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			getWorkspaceErrors: map[logicalcluster.Path]error{logicalcluster.NewPath("root:foo"): errors.NewForbidden(schema.GroupResource{}, "bar", fmt.Errorf("not allowed"))},
			discoveryErrors: map[logicalcluster.Path]error{
				logicalcluster.NewPath("root:foo:foe"): errors.NewForbidden(schema.GroupResource{}, "", fmt.Errorf("forbidden")),
			},
			param:      "root:foo:foe",
			wantErr:    true,
			wantErrors: []string{"access to workspace \"root:foo:foe\" denied"},
		},
		{
			name:   "invalid workspace name format",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:      "ju:nk§",
			wantErr:    true,
			wantErrors: []string{"invalid workspace path: ju:nk§"},
		},
		{
			name:    "absolute name, no cluster URL",
			config:  *NewKubeconfig().WithKcpCurrent("").Build(),
			param:   "root:bar",
			wantErr: true,
		},
		{
			name:        ":system:admin",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("system:admin"),
			param:       ":system:admin",
			expected:    NewKubeconfig().WithKcpCurrent("system:admin").WithKcpPrevious("root:foo").Build(),
			destination: "system:admin",
			wantStdout:  []string{"Current workspace is 'system:admin'"},
		},
		{
			name:        "root",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("root"),
			param:       "root",
			expected:    NewKubeconfig().WithKcpCurrent("root").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root'"},
		},
		{
			name:    "root, no cluster URL",
			config:  *NewKubeconfig().WithKcpCurrent("").Build(),
			param:   "root",
			wantErr: true,
		},
		{
			name:   "..",
			config: *NewKubeconfig().WithKcpCurrent("root:foo:bar").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root"): {"foo"},
			},
			discovery:   discoveryFor("root:foo"),
			param:       "..",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root:foo:bar").Build(),
			destination: "root:foo",
			wantStdout:  []string{"Current workspace is 'root:foo'"},
		},
		{
			name:    ".., no cluster URL",
			config:  *NewKubeconfig().WithKcpCurrent("").Build(),
			param:   "..",
			wantErr: true,
		},
		{
			name:        ".. to root",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("root"),
			param:       "..",
			expected:    NewKubeconfig().WithKcpCurrent("root").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root'"},
		},
		{
			name:        ".. slash .. to child of root",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").Build(),
			discovery:   discoveryFor("root:foo"),
			param:       "../..",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root:foo:bar:baz").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root:foo'"},
		},
		{
			name:        "..:.. to child of root",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").Build(),
			discovery:   discoveryFor("root:foo"),
			param:       "..:..",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root:foo:bar:baz").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root:foo'"},
		},
		{
			name:     ".. in root",
			config:   *NewKubeconfig().WithKcpCurrent("root").Build(),
			param:    "..",
			expected: nil,
			wantErr:  true,
		},
		{
			name:        "relative",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("root:foo:bar:baz"),
			param:       "bar:baz",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root:foo:bar:baz'"},
		},
		{
			name:        "relative with ..",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("root:foo:bar:baz"),
			param:       "bar:..:bar:baz:..:baz",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root:foo:bar:baz'"},
		},
		{
			name:        "relative with .",
			config:      *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			discovery:   discoveryFor("root:foo:bar:baz"),
			param:       ".:bar:.:.:baz:.",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root:foo:bar:baz'"},
		},
		{
			name:   "- with existing previous context",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root:foo:bar").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       "-",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			wantStdout:  []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "- without existing previous context",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:   "-",
			wantErr: true,
		},
		{
			name: "- with non-cluster context",
			config: clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current":  {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"workspace.kcp.io/previous": {Cluster: "other", AuthInfo: "other"},
					"other":                     {Cluster: "other", AuthInfo: "other"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo"},
					"other":                    {Server: "https://other/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"other": {Token: "test"},
				},
			},
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param: "-",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current":  {Cluster: "other", AuthInfo: "other"},
					"workspace.kcp.io/previous": {Cluster: "workspace.kcp.io/previous", AuthInfo: "test"},
					"other":                     {Cluster: "other", AuthInfo: "other"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current":  {Server: "https://test/clusters/root:foo"},
					"workspace.kcp.io/previous": {Server: "https://test/clusters/root:foo"},
					"other":                     {Server: "https://other/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":  {Token: "test"},
					"other": {Token: "test"},
				},
			},
			wantStdout: []string{"Current workspace is the URL \"https://other/\""},
		},
		{
			name: "- with non-cluster context, with non-cluster previous context",
			config: clientcmdapi.Config{CurrentContext: "other",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current":  {Cluster: "workspace.kcp.io/current", AuthInfo: "test"},
					"workspace.kcp.io/previous": {Cluster: "other2", AuthInfo: "other2"},
					"other":                     {Cluster: "other", AuthInfo: "other"},
					"other2":                    {Cluster: "other2", AuthInfo: "other2"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo"},
					"other":                    {Server: "https://other/"},
					"other2":                   {Server: "https://other2/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":   {Token: "test"},
					"other":  {Token: "test"},
					"other2": {Token: "test2"},
				},
			},
			param: "-",
			expected: &clientcmdapi.Config{CurrentContext: "workspace.kcp.io/current",
				Contexts: map[string]*clientcmdapi.Context{
					"workspace.kcp.io/current":  {Cluster: "other2", AuthInfo: "other2"},
					"workspace.kcp.io/previous": {Cluster: "other", AuthInfo: "other"},
					"other":                     {Cluster: "other", AuthInfo: "other"},
					"other2":                    {Cluster: "other2", AuthInfo: "other2"},
				},
				Clusters: map[string]*clientcmdapi.Cluster{
					"workspace.kcp.io/current": {Server: "https://test/clusters/root:foo"},
					"other":                    {Server: "https://other/"},
					"other2":                   {Server: "https://other2/"},
				},
				AuthInfos: map[string]*clientcmdapi.AuthInfo{
					"test":   {Token: "test"},
					"other":  {Token: "test"},
					"other2": {Token: "test2"},
				},
			},
			wantStdout: []string{"Current workspace is the URL \"https://other2/\""},
		},
		{
			name:   "~",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd:user-name"),
			param:       "~",
			expected:    NewKubeconfig().WithKcpCurrent(homeWorkspace.String()).WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			wantStdout:  []string{fmt.Sprintf("Current workspace is '%s'", homeWorkspace.String())},
		},
		{
			name:   "~/bar/baz",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd:user-name:bar:baz"),
			param:       "~/bar/baz",
			expected:    NewKubeconfig().WithKcpCurrent(homeWorkspace.String() + ":bar:baz").WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			wantStdout:  []string{fmt.Sprintf("Current workspace is '%s'", homeWorkspace.String()+":bar:baz")},
		},
		{
			name:   "~/..",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd"),
			param:       "~/..",
			expected:    NewKubeconfig().WithKcpCurrent("root:users:ab:cd").WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			wantStdout:  []string{"Current workspace is 'root:users:ab:cd'"},
		},
		{
			name:   "~ unfolded",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd:user-name"),
			param:       "/home/sts",
			expected:    NewKubeconfig().WithKcpCurrent(homeWorkspace.String()).WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			wantStdout:  []string{fmt.Sprintf("Current workspace is '%s'", homeWorkspace.String())},
		},
		{
			name:   "no arg",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd:user-name"),
			param:       "",
			expected:    NewKubeconfig().WithKcpCurrent(homeWorkspace.String()).WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			wantStderr: []string{
				"Note: 'kubectl ws' now matches 'cd' semantics: go to home workspace. 'kubectl ws -' to go back. 'kubectl ws .' to print current workspace.",
			},
			wantStdout: []string{fmt.Sprintf("Current workspace is '%s'.", homeWorkspace.String())},
		},
		{
			name:   "workspace name, apibindings have matching permission and export claims",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimAccepted).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, apibindings don't have matching permission or export claims",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", "").
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.\n",
			},
			wantStdout: []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "~, apibinding claims/exports don't match",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				core.RootCluster: {"~"},
			},
			discovery:   discoveryFor("root:users:ab:cd:user-name"),
			param:       "~",
			expected:    NewKubeconfig().WithKcpCurrent(homeWorkspace.String()).WithKcpPrevious("root:foo").Build(),
			destination: homeWorkspace.String(),
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimAccepted).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
			},
			wantStdout: []string{
				fmt.Sprintf("Current workspace is '%s'", homeWorkspace.String())},
		},
		{
			name:   "- with existing previous context, apibinding claims/exports don't match ",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root:foo:bar").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       "-",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimAccepted).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, apibindings rejected",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimRejected).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
			noWarn: true,
		},
		{
			name:   "workspace name, apibindings accepted",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimAccepted).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					Build(),
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
			noWarn: true,
		},
		{
			name:   "workspace name, some apibindings accepted",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", apisv1alpha2.ClaimAccepted).
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, apibindings not acknowledged",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", "").
					WithExportClaim("test.kcp.io", "test", "abcdef").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for test.test.kcp.io:abcdef specified on APIBinding a but not accepted or rejected.",
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, APIBindings unacknowledged and unspecified",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", "").
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
				"Warning: claim for test.test.kcp.io:abcdef specified on APIBinding a but not accepted or rejected.",
			},
			wantStdout: []string{
				"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, multiple APIBindings unacknowledged",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithPermissionClaim("test.kcp.io", "test", "abcdef", "").
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithPermissionClaim("test2.kcp.io", "test2", "abcdef", "").
					WithExportClaim("test2.kcp.io", "test2", "abcdef").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for test.test.kcp.io:abcdef specified on APIBinding a but not accepted or rejected.",
				"Warning: claim for test2.test2.kcp.io:abcdef specified on APIBinding a but not accepted or rejected.",
			},
			wantStdout: []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "workspace name, multiple APIBindings unspecified",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			discovery:   discoveryFor("root:foo:bar"),
			param:       "bar",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar",
			apiBindings: []apisv1alpha2.APIBinding{
				newBindingBuilder("a").
					WithExportClaim("test.kcp.io", "test", "abcdef").
					WithExportClaim("", "configmaps", "").
					Build(),
			},
			wantStderr: []string{
				"Warning: claim for configmaps exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
				"Warning: claim for test.test.kcp.io:abcdef exported but not specified on APIBinding a\nAdd this claim to the APIBinding's Spec.",
			},
			wantStdout: []string{"Current workspace is 'root:foo:bar'"},
		},
		{
			name:   "relative change multiple jumps from non root",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").WithKcpPrevious("root").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"):     {"bar"},
				logicalcluster.Name("root:foo:bar"): {"baz"},
			},
			discovery:   discoveryFor("root:foo:bar:baz"),
			param:       "bar:baz",
			expected:    NewKubeconfig().WithKcpCurrent("root:foo:bar:baz").WithKcpPrevious("root:foo").Build(),
			destination: "root:foo:bar:baz",
			wantStdout: []string{
				"Current workspace is 'root:foo:bar:baz'"},
		},
		{
			name:   ": real root",
			config: *NewKubeconfig().WithKcpCurrent("root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       ":",
			expected:    NewKubeconfig().WithKcpCurrent("root").WithKcpPrevious("root:foo").Build(),
			destination: "root",
			wantStdout:  []string{"Current workspace is 'root'"},
		},
		{
			name:   ": custom 'my-root'",
			config: *NewKubeconfig().WithKcpCurrent("my-root:foo").Build(),
			existingWorkspaces: map[logicalcluster.Name][]string{
				logicalcluster.Name("root:foo"): {"bar"},
			},
			param:       ":",
			expected:    NewKubeconfig().WithKcpCurrent("my-root").WithKcpPrevious("my-root:foo").Build(),
			destination: "my-root",
			wantStdout:  []string{"Current workspace is 'my-root'"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *clientcmdapi.Config

			cluster := tt.config.Clusters[tt.config.Contexts[tt.config.CurrentContext].Cluster]
			u := parseURLOrDie(cluster.Server)
			u.Path = ""

			objs := []runtime.Object{}
			for lcluster, names := range tt.existingWorkspaces {
				for _, name := range names {
					obj := &tenancyv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name:        name,
							Annotations: map[string]string{logicalcluster.AnnotationKey: lcluster.String()},
						},
						Spec: tenancyv1alpha1.WorkspaceSpec{
							Type: &tenancyv1alpha1.WorkspaceTypeReference{
								Name: "universal",
								Path: "root",
							},
						},
					}
					if !tt.unready[lcluster.Path()][name] {
						obj.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
						obj.Spec.URL = fmt.Sprintf("https://test%s", lcluster.Path().Join(name).RequestPath())
					}
					objs = append(objs, obj)
				}
			}
			client := kcpfakeclient.NewSimpleClientset(objs...)
			client.PrependReactor("get", "workspaces", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
				getAction := action.(kcptesting.GetAction)
				if getAction.GetCluster() != core.RootCluster.Path() {
					return false, nil, nil
				}
				if getAction.GetName() == "~" {
					return true, &tenancyv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "user-name",
						},
						Spec: tenancyv1alpha1.WorkspaceSpec{
							URL: fmt.Sprintf("https://test%s", homeWorkspace.RequestPath()),
							Type: &tenancyv1alpha1.WorkspaceTypeReference{
								Name: "home",
								Path: "root",
							},
						},
					}, nil
				}
				return false, nil, nil
			})

			// return nothing in the default case.
			getAPIBindings := func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha2.APIBinding, error) {
				return nil, nil
			}

			if tt.destination != "" {
				// Add APIBindings to our Clientset if we have them
				if len(tt.apiBindings) > 0 {
					getAPIBindings = func(ctx context.Context, kcpClusterClient kcpclientset.ClusterInterface, host string) ([]apisv1alpha2.APIBinding, error) {
						return tt.apiBindings, nil
					}
				}
			}

			client.PrependReactor("get", "workspaces", func(action kcptesting.Action) (bool, runtime.Object, error) {
				err, recorded := tt.getWorkspaceErrors[action.GetCluster()]
				return recorded, nil, err
			})

			for lcluster, d := range tt.discovery {
				if client.Resources == nil {
					client.Resources = map[logicalcluster.Path][]*metav1.APIResourceList{}
				}
				client.Resources[lcluster] = d
			}

			streams, _, stdout, stderr := genericclioptions.NewTestIOStreams()
			opts := NewUseWorkspaceOptions(streams)
			opts.Name = tt.param
			opts.ShortWorkspaceOutput = tt.short
			opts.modifyConfig = func(configAccess clientcmd.ConfigAccess, config *clientcmdapi.Config) error {
				got = config
				return nil
			}
			opts.getAPIBindings = getAPIBindings
			opts.kcpClusterClient = fakeClusterClientWithDiscoveryErrors{
				ClusterClientset: client,
				discoveryErrs:    tt.discoveryErrors,
			}
			opts.ClientConfig = clientcmd.NewDefaultClientConfig(*tt.config.DeepCopy(), nil)
			opts.startingConfig = &tt.config
			opts.userHomeDir = func() (string, error) { return "/home/sts", nil }
			err := opts.Run(context.Background())
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			t.Logf("stdout:\n%s", stdout.String())
			t.Logf("stderr:\n%s", stderr.String())

			if got != nil && tt.expected == nil {
				t.Errorf("unexpected kubeconfig write")
			} else if got == nil && tt.expected != nil {
				t.Errorf("expected a kubeconfig write, but didn't see one")
			} else if got != nil && !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("unexpected config, diff (expected, got): %s", cmp.Diff(tt.expected, got))
			}

			for _, s := range tt.wantStdout {
				require.Contains(t, stdout.String(), s)
			}
			if tt.noWarn {
				require.NotContains(t, stderr.String(), "Warning")
			}
			for _, s := range tt.wantStderr {
				require.Contains(t, stderr.String(), s)
			}
			if err != nil {
				for _, s := range tt.wantErrors {
					require.Contains(t, err.Error(), s)
				}
			}
		})
	}
}

type kubeconfigBuilder struct {
	config clientcmdapi.Config
}

func NewKubeconfig() *kubeconfigBuilder {
	return &kubeconfigBuilder{}
}

func (b *kubeconfigBuilder) WithKcpCurrent(pth string) *kubeconfigBuilder {
	b.config.CurrentContext = "workspace.kcp.io/current"
	if b.config.Contexts == nil {
		b.config.Contexts = map[string]*clientcmdapi.Context{}
	}
	b.config.Contexts["workspace.kcp.io/current"] = &clientcmdapi.Context{Cluster: "workspace.kcp.io/current", AuthInfo: "test"}
	if b.config.Clusters == nil {
		b.config.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	b.config.Clusters["workspace.kcp.io/current"] = &clientcmdapi.Cluster{Server: "https://test/clusters/" + pth}
	if b.config.AuthInfos == nil {
		b.config.AuthInfos = map[string]*clientcmdapi.AuthInfo{}
	}
	b.config.AuthInfos["test"] = &clientcmdapi.AuthInfo{Token: "test"}
	return b
}

func (b *kubeconfigBuilder) WithKcpPrevious(pth string) *kubeconfigBuilder {
	if b.config.Contexts == nil {
		b.config.Contexts = map[string]*clientcmdapi.Context{}
	}
	b.config.Contexts["workspace.kcp.io/previous"] = &clientcmdapi.Context{Cluster: "workspace.kcp.io/previous", AuthInfo: "test"}
	if b.config.Clusters == nil {
		b.config.Clusters = map[string]*clientcmdapi.Cluster{}
	}
	b.config.Clusters["workspace.kcp.io/previous"] = &clientcmdapi.Cluster{Server: "https://test/clusters/" + pth}
	if b.config.AuthInfos == nil {
		b.config.AuthInfos = map[string]*clientcmdapi.AuthInfo{}
	}
	b.config.AuthInfos["test"] = &clientcmdapi.AuthInfo{Token: "test"}
	return b
}

func (b *kubeconfigBuilder) Build() *clientcmdapi.Config {
	return &b.config
}

type fakeClusterClientWithDiscoveryErrors struct {
	*kcpfakeclient.ClusterClientset
	discoveryErrs map[logicalcluster.Path]error
}

func (f fakeClusterClientWithDiscoveryErrors) Cluster(cluster logicalcluster.Path) kcpclient.Interface {
	return fakeClientWithDiscoveryErrors{f.ClusterClientset.Cluster(cluster), f.discoveryErrs[cluster]}
}

type fakeClientWithDiscoveryErrors struct {
	kcpclient.Interface

	err error
}

func (c fakeClientWithDiscoveryErrors) Discovery() discovery.DiscoveryInterface {
	d := c.Interface.Discovery()
	return errorDiscoveryClient{d, c.err}
}

type errorDiscoveryClient struct {
	discovery.DiscoveryInterface
	err error
}

func (c errorDiscoveryClient) ServerGroups() (*metav1.APIGroupList, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.DiscoveryInterface.ServerGroups()
}

type bindingBuilder struct {
	apisv1alpha2.APIBinding
}

func newBindingBuilder(name string) *bindingBuilder {
	b := new(bindingBuilder)
	b.ObjectMeta = metav1.ObjectMeta{
		Name: name,
	}
	return b
}

func (b *bindingBuilder) WithPermissionClaim(group, resource, identityHash string, state apisv1alpha2.AcceptablePermissionClaimState) *bindingBuilder {
	if len(b.Spec.PermissionClaims) == 0 {
		b.Spec.PermissionClaims = make([]apisv1alpha2.AcceptablePermissionClaim, 0)
	}

	pc := apisv1alpha2.AcceptablePermissionClaim{
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    group,
					Resource: resource,
				},
				IdentityHash: identityHash,
			},
		},
	}

	if state != "" {
		pc.State = state
	}

	b.Spec.PermissionClaims = append(b.Spec.PermissionClaims, pc)
	return b
}

func (b *bindingBuilder) WithExportClaim(group, resource, identityHash string) *bindingBuilder {
	if len(b.Status.ExportPermissionClaims) == 0 {
		b.Status.ExportPermissionClaims = make([]apisv1alpha2.PermissionClaim, 0)
	}

	pc := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    group,
			Resource: resource,
		},
		IdentityHash: identityHash,
	}

	b.Status.ExportPermissionClaims = append(b.Status.ExportPermissionClaims, pc)
	return b
}

func (b *bindingBuilder) Build() apisv1alpha2.APIBinding {
	return b.APIBinding
}
