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

package apifixtures

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// BindToExport creates an APIBinding in bindingClusterName that points at apiExportName in exportClusterName. It waits
// up to wait.ForeverTestTimeout for the APIBinding to have its apisv1alpha1.InitialBindingCompleted status.
func BindToExport(
	ctx context.Context,
	t *testing.T,
	exportClusterName logicalcluster.Name,
	apiExportName string,
	bindingClusterName logicalcluster.Name,
	clusterClient kcpclientset.ClusterInterface,
) {
	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.ReplaceAll(exportClusterName.String(), ":", "-"),
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Cluster: exportClusterName.String(),
					Name:    apiExportName,
				},
			},
		},
	}

	framework.Eventually(t, func() (bool, string) {
		t.Logf("Creating APIBinding %s|%s", bindingClusterName, binding.Name)
		_, err := clusterClient.Cluster(bindingClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating APIBinding %s|%s: %v", bindingClusterName, binding.Name, err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	framework.Eventually(t, func() (bool, string) {
		b, err := clusterClient.Cluster(bindingClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		require.NoError(t, err, "error getting APIBinding %s|%s", bindingClusterName, binding.Name)

		return conditions.IsTrue(b, apisv1alpha1.InitialBindingCompleted), toYAML(t, b.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func toYAML(t *testing.T, obj interface{}) string {
	bs, err := yaml.Marshal(obj)
	require.NoError(t, err, "error converting to YAML")
	return string(bs)
}
