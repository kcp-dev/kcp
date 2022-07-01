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

package wildwest

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

func Create(t *testing.T, client apiextensionsv1client.CustomResourceDefinitionInterface, grs ...metav1.GroupResource) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	t.Cleanup(cancelFunc)

	err := configcrds.CreateFromFS(ctx, client, rawCustomResourceDefinitions, grs...)
	require.NoError(t, err)
}

func CustomResourceDefinition(gr metav1.GroupResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := rawCustomResourceDefinitions.ReadFile(fmt.Sprintf("%s_%s.yaml", gr.Group, gr.Resource))
	if err != nil {
		return nil, fmt.Errorf("could not read CRD %s: %w", gr.String(), err)
	}

	expectedGvk := &schema.GroupVersionKind{Group: apiextensionsv1.GroupName, Version: "v1", Kind: "CustomResourceDefinition"}

	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw CRD %s: %w", gr.String(), err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded CRD %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}

	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil, fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gr.String(), crd, &apiextensionsv1.CustomResourceDefinition{})
	}

	return crd, nil
}

var invalidCharacters = regexp.MustCompile(`[^a-z0-9\-]`)

// Export creates an APIExport for Cowboys in the provided cluster and waits for the export to have virtual workspace URLs.
func Export(ctx context.Context, t *testing.T, server framework.RunningServer, installInto logicalcluster.Name) *apisv1alpha1.APIExport {
	t.Logf("Installing Cowboy APIExport in cluster %q", installInto)
	gr := metav1.GroupResource{
		Group:    wildwest.GroupName,
		Resource: "cowboys",
	}
	crd, err := CustomResourceDefinition(gr)
	require.NoError(t, err, "could not determine CustomResourceDefinition")

	resourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, invalidCharacters.ReplaceAllString(strings.ToLower(t.Name()), "-"))
	require.NoError(t, err, "could not convert CustomResourceDefinition to APIResourceSchema")

	kcpClusterClient, err := kcpclient.NewClusterForConfig(server.DefaultConfig(t))
	require.NoError(t, err, "could not create KCP cluster client")

	kcpClient := kcpClusterClient.Cluster(installInto)
	apisClient := kcpClient.ApisV1alpha1()
	resourceSchema, err = apisClient.APIResourceSchemas().Create(ctx, resourceSchema, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		require.NoError(t, err, "could not create APIResourceSchema")
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return apisClient.APIResourceSchemas().Get(ctx, resourceSchema.Name, metav1.GetOptions{})
	})
	t.Logf("Created APIResourceSchema %q|%q", installInto.String(), resourceSchema.Name)

	export, err := apisClient.APIExports().Create(ctx, &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: gr.String(),
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{resourceSchema.Name},
		},
	}, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		require.NoError(t, err, "could not create APIExport")
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return apisClient.APIExports().Get(ctx, export.Name, metav1.GetOptions{})
	})
	t.Logf("Created APIExport %q|%q", installInto.String(), export.Name)

	framework.Eventually(t, func() (bool, string) {
		export, err = apisClient.APIExports().Get(ctx, export.Name, metav1.GetOptions{})
		require.NoError(t, err, "Error fetching APIExport %q|%q", installInto.String(), export.Name)
		done := conditions.IsTrue(export, apisv1alpha1.APIExportVirtualWorkspaceURLsReady)
		var reason string
		if !done {
			condition := conditions.Get(export, apisv1alpha1.APIExportVirtualWorkspaceURLsReady)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for APIExport %q|%q virtual workspace URLs to be ready: %s: %s", installInto.String(), export.Name, condition.Reason, condition.Message)
			} else {
				reason = fmt.Sprintf("Not done waiting for APIExport %q|%q virtual workspace URLs to be ready: no condition present", installInto.String(), export.Name)
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for virtual workspace URLs to be ready on APIExport")
	t.Logf("APIExport %q|%q virtual workspace URLs are ready", installInto.String(), export.Name)

	return export
}

// BindingFor returns an APIBinding referencing the APIExport.
func BindingFor(export *apisv1alpha1.APIExport) *apisv1alpha1.APIBinding {
	return &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: export.Name,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       logicalcluster.From(export).String(),
					ExportName: export.Name,
				},
			},
		},
	}
}

// Bind binds to the APIExport in the given logical cluster and waits for the new API to bind.
func Bind(ctx context.Context, t *testing.T, server framework.RunningServer, export *apisv1alpha1.APIExport, installInto logicalcluster.Name) {
	kcpClusterClient, err := kcpclient.NewClusterForConfig(server.DefaultConfig(t))
	require.NoError(t, err, "could not create KCP cluster client")

	kcpClient := kcpClusterClient.Cluster(installInto)
	apisClient := kcpClient.ApisV1alpha1()
	binding, err := apisClient.APIBindings().Create(ctx, BindingFor(export), metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		require.NoError(t, err, "could not create APIBinding")
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return apisClient.APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
	})
	t.Logf("Created APIBinding %q|%q", installInto.String(), binding.Name)

	framework.Eventually(t, func() (bool, string) {
		binding, err = apisClient.APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		require.NoError(t, err, "Error fetching APIBinding %q|%q", installInto.String(), binding.Name)
		done := conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted)
		var reason string
		if !done {
			condition := conditions.Get(binding, apisv1alpha1.InitialBindingCompleted)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for APIBinding %q|%q to be bound: %s: %s", installInto.String(), binding.Name, condition.Reason, condition.Message)
			} else {
				reason = fmt.Sprintf("Not done waiting for APIBinding %q|%q to be bound: no condition present", installInto.String(), binding.Name)
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIBinding to be bound")
	t.Logf("APIBinding %q|%q is bound", installInto.String(), binding.Name)
}
