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

package root

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

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

//go:embed *.yaml
var fs embed.FS

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, kcpClient kcpclient.Interface, rootDiscoveryClient discovery.DiscoveryInterface, rootDynamicClient dynamic.Interface, homeWorkspaceCreatorGroups []string, batteriesIncluded sets.String) error {
	homeWorkspaceCreatorGroupReplacement := ""
	for _, group := range homeWorkspaceCreatorGroups {
		homeWorkspaceCreatorGroupReplacement += `
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: ` + group
	}
	if homeWorkspaceCreatorGroupReplacement == "" {
		homeWorkspaceCreatorGroupReplacement = "[]"
	}

	if err := confighelpers.Bootstrap(ctx, rootDiscoveryClient, rootDynamicClient, batteriesIncluded, fs, confighelpers.ReplaceOption(
		"HOME_CREATOR_GROUPS", homeWorkspaceCreatorGroupReplacement,
	)); err != nil {
		return err
	}

	// set LogicalCluster to Ready
	return wait.PollImmediateUntilWithContext(ctx, time.Millisecond*100, func(ctx context.Context) (done bool, err error) {
		logger := klog.FromContext(ctx).WithValues("bootstrapping", "root-phase0")
		this, err := kcpClient.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if err != nil {
			logger.Error(err, "failed to get this workspace in root")
			return false, nil
		}
		if this.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing {
			this.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
			_, err = kcpClient.CoreV1alpha1().LogicalClusters().UpdateStatus(ctx, this, metav1.UpdateOptions{})
			if err != nil {
				logger.Error(err, "failed to update this workspace status in root")
				return false, nil
			}
		}
		return true, nil
	})
}
