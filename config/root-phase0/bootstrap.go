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

package rootphase0

import (
	"context"
	"embed"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, kcpClient kcpclient.Interface, rootDiscoveryClient discovery.DiscoveryInterface, rootDynamicClient dynamic.Interface, batteriesIncluded sets.Set[string]) error {
	if err := confighelpers.BindRootAPIs(ctx, kcpClient, "shards.core.kcp.io", "tenancy.kcp.io", "topology.kcp.io"); err != nil {
		return err
	}
	err := confighelpers.Bootstrap(ctx, rootDiscoveryClient, rootDynamicClient, batteriesIncluded, fs)
	if err != nil {
		return err
	}

	// set LogicalCluster to Initializing
	return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (done bool, err error) {
		logger := klog.FromContext(ctx).WithValues("bootstrapping", "root-phase0")
		logicalCluster, err := kcpClient.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "failed to get this workspace in root")
			return false, nil
		}
		if logicalCluster.Status.Phase == corev1alpha1.LogicalClusterPhaseScheduling {
			logicalCluster.Status.Phase = corev1alpha1.LogicalClusterPhaseInitializing
			_, err = kcpClient.CoreV1alpha1().LogicalClusters().UpdateStatus(ctx, logicalCluster, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "failed to update LogicalCluster root:cluster")
				return false, nil
			}
		}
		return true, nil
	})
}

// Unmarshal YAML-decodes the give embedded file name into the target.
func Unmarshal(fileName string, o interface{}) error {
	bs, err := fs.ReadFile(fileName)
	if err != nil {
		return err
	}

	return yaml.Unmarshal(bs, o)
}
