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

package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xlab/treeprint"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/cli/pkg/base"
	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpfakeclient "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
)

// newTreeClientConfig creates a minimal clientcmd.ClientConfig pointing at the given server URL.
func newTreeClientConfig(serverURL string) clientcmd.ClientConfig {
	cfg := clientcmdapi.Config{
		CurrentContext: "test",
		Contexts:       map[string]*clientcmdapi.Context{"test": {Cluster: "test", AuthInfo: "test"}},
		Clusters:       map[string]*clientcmdapi.Cluster{"test": {Server: serverURL}},
		AuthInfos:      map[string]*clientcmdapi.AuthInfo{"test": {Token: "test"}},
	}
	return clientcmd.NewDefaultClientConfig(cfg, &clientcmd.ConfigOverrides{})
}

// newTreeOptions builds a TreeOptions wired up with a fake kcp client and the given IOStreams.
// Call it with a pre-built fakeClient so callers can prime reactors / objects before passing it in.
func newTreeOptions(streams genericclioptions.IOStreams, serverURL string, objs ...runtime.Object) *TreeOptions {
	o := NewTreeOptions(streams)
	o.Options = base.NewOptions(streams)
	o.ClientConfig = newTreeClientConfig(serverURL)
	o.kcpClusterClient = kcpfakeclient.NewClientset(objs...)
	return o
}

// TestPopulateBranch_MountWorkspace verifies that a workspace whose URL cannot
// be parsed as a /clusters/ path AND whose Spec.Mount is non-nil is rendered
// as a "<name> [m]" leaf instead of causing an error.
func TestPopulateBranch_MountWorkspace(t *testing.T) {
	nonStandardURL := "https://proxy.example.com/services/site-proxy/foo"

	mountWS := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "mounted-ws",
			// The fake kcp cluster client routes list results using this annotation.
			Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			URL:   nonStandardURL,
			Mount: &tenancyv1alpha1.Mount{},
		},
	}

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     strings.NewReader(""),
		Out:    out,
		ErrOut: errOut,
	}

	o := newTreeOptions(streams, "https://test/clusters/root", mountWS)

	tree := treeprint.New()
	err := o.populateBranch(context.Background(), tree, logicalcluster.NewPath("root"), "root")
	require.NoError(t, err, "populateBranch should not error for a mount workspace")

	treeStr := tree.String()
	require.Contains(t, treeStr, "mounted-ws [m]",
		"expected mount workspace to appear as '<name> [m]' leaf in tree output")
}

// TestPopulateBranch_NonMountURLError verifies that a workspace with a
// non-standard URL but a nil Mount causes populateBranch to return an error
// (preserving the existing error-surfacing behaviour for non-mount URLs).
func TestPopulateBranch_NonMountURLError(t *testing.T) {
	nonStandardURL := "https://proxy.example.com/services/site-proxy/foo"

	regularWS := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "bad-ws",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			URL:   nonStandardURL,
			Mount: nil, // not a mount workspace
		},
	}

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     strings.NewReader(""),
		Out:    out,
		ErrOut: errOut,
	}

	o := newTreeOptions(streams, "https://test/clusters/root", regularWS)

	tree := treeprint.New()
	err := o.populateBranch(context.Background(), tree, logicalcluster.NewPath("root"), "root")
	require.Error(t, err, "expected error when workspace URL is non-standard and Spec.Mount is nil")
}

// TestPopulateBranch_NormalWorkspace verifies that a well-formed workspace URL
// is traversed normally without error.
func TestPopulateBranch_NormalWorkspace(t *testing.T) {
	child := &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "child",
			Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			URL: "https://test/clusters/root:child",
		},
	}

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     strings.NewReader(""),
		Out:    out,
		ErrOut: errOut,
	}

	o := newTreeOptions(streams, "https://test/clusters/root", child)

	tree := treeprint.New()
	err := o.populateBranch(context.Background(), tree, logicalcluster.NewPath("root"), "root")
	require.NoError(t, err)

	treeStr := tree.String()
	require.Contains(t, treeStr, "child",
		"expected normal workspace to appear in tree output")
	require.NotContains(t, treeStr, "[m]",
		"expected normal workspace NOT to be marked as [m]")
}

// TestRun_MountContextURL verifies that when the kubeconfig host points to a
// non-standard (mount) URL, Run does not return an error; instead it prints a
// message to Out and returns immediately (no fallback to root).
func TestRun_MountContextURL(t *testing.T) {
	mountHostURL := "https://proxy.example.com/services/site-proxy/foo"

	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     strings.NewReader(""),
		Out:    out,
		ErrOut: errOut,
	}

	// No workspaces — we only care about the URL handling behaviour.
	o := newTreeOptions(streams, mountHostURL)

	err := o.Run(context.Background())
	require.NoError(t, err, "Run should not error when context URL is a mount URL")
	require.Contains(t, out.String(), "does not point directly to a kcp workspace",
		"expected informational message about non-kcp URL on Out")
}
