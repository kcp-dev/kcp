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

package plugin

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// Cordon the workload cluster and mark it as unschedulable
func (c *Config) Cordon(ctx context.Context, workloadClusterName string) error {
	config, err := clientcmd.NewDefaultClientConfig(*c.startingConfig, c.overrides).ClientConfig()
	if err != nil {
		return err
	}

	err = modifyCordon(ctx, config, workloadClusterName, true)
	if err != nil {
		return err
	}

	return nil
}

// Uncordon the workload cluster and mark it as schedulable
func (c *Config) Uncordon(ctx context.Context, workloadClusterName string) error {
	config, err := clientcmd.NewDefaultClientConfig(*c.startingConfig, c.overrides).ClientConfig()
	if err != nil {
		return err
	}

	err = modifyCordon(ctx, config, workloadClusterName, false)
	if err != nil {
		return err
	}

	return nil
}

// change the workload cluster cordon value
func modifyCordon(ctx context.Context, config *rest.Config, workloadClusterName string, cordon bool) error {
	kcpClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	workloadCluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx,
		workloadClusterName,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get WorkloadCluster %s: %w", workloadClusterName, err)
	}

	// See if there is nothing to do
	if cordon && workloadCluster.Spec.Unschedulable {
		fmt.Println(workloadClusterName, "already cordoned")
		return nil
	} else if !cordon && !workloadCluster.Spec.Unschedulable {
		fmt.Println(workloadClusterName, "already uncordoned")
		return nil
	}

	var patchBytes []byte
	if cordon {
		patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":true}]`)

	} else {
		evict := ``
		if workloadCluster.Spec.EvictAfter != nil {
			evict = `,{"op":"remove","path":"/spec/evictAfter"}`
		}

		patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":false}` + evict + `]`)
	}

	//fmt.Printf("patchBytes %s", patchBytes)

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Patch(ctx, workloadClusterName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to update WorkloadCluster %s: %w", workloadClusterName, err)
	}

	if cordon {
		fmt.Println(workloadClusterName, "cordoned")
	} else {
		fmt.Println(workloadClusterName, "uncordoned")
	}

	return nil
}

// Start draining the workload cluster and mark it as unschedulable
func (c *Config) Drain(ctx context.Context, workloadClusterName string) error {
	config, err := clientcmd.NewDefaultClientConfig(*c.startingConfig, c.overrides).ClientConfig()
	if err != nil {
		return err
	}

	kcpClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	workloadCluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx,
		workloadClusterName,
		metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to get workloadcluster %s: %w", workloadClusterName, err)
	}

	// See if there is nothing to do
	if workloadCluster.Spec.EvictAfter != nil && workloadCluster.Spec.Unschedulable {
		fmt.Println(workloadClusterName, "already draining")
		return nil
	}

	nowTime := time.Now().UTC()
	var patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":true},{"op":"replace","path":"/spec/evictAfter","value":"` + nowTime.Format(time.RFC3339) + `"}]`)

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Patch(ctx, workloadClusterName, types.JSONPatchType, patchBytes, metav1.PatchOptions{})

	if err != nil {
		return fmt.Errorf("failed to update WorkloadCluster %s: %w", workloadClusterName, err)
	}

	fmt.Println(workloadClusterName, "draining")

	return nil

}
