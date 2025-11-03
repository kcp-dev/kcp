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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
)

// BindToExport creates an APIBinding in bindingClusterName that points at apiExportName in exportClusterName. It waits
// up to wait.ForeverTestTimeout for the APIBinding to have its apisv1alpha2.InitialBindingCompleted status.
func BindToExport(
	ctx context.Context,
	t *testing.T,
	exportPath logicalcluster.Path,
	apiExportName string,
	bindingClusterName logicalcluster.Path,
	clusterClient kcpclientset.ClusterInterface,
) {
	t.Helper()

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiExportName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: exportPath.String(),
					Name: apiExportName,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		t.Logf("Creating APIBinding %s|%s", bindingClusterName, binding.Name)
		_, err := clusterClient.Cluster(bindingClusterName).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating APIBinding %s|%s: %v", bindingClusterName, binding.Name, err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return clusterClient.Cluster(bindingClusterName).ApisV1alpha2().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))
}
