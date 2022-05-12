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
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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

	// Get a JSON copy of the existing data
	oldData, err := json.Marshal(workloadCluster)
	if err != nil {
		return fmt.Errorf("failed to marshal old data for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	// See if there is nothing to do
	if cordon && workloadCluster.Spec.Unschedulable {
		fmt.Println(workloadClusterName, "already cordoned")
		return nil
	} else if !cordon && !workloadCluster.Spec.Unschedulable {
		fmt.Println(workloadClusterName, "already uncordoned")
		return nil
	}

	// Build a JSON copy of the changed data
	newObj := workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			UID:             workloadCluster.UID,
			ResourceVersion: workloadCluster.ResourceVersion,
			Name:            workloadClusterName,
		},
		Spec: workloadv1alpha1.WorkloadClusterSpec{
			Unschedulable: cordon,
		},
	}

	//if we are turning off cordon, make sure evictAfter is mil
	if (!cordon) && (workloadCluster.Spec.EvictAfter != nil) {
		newObj.Spec.EvictAfter = nil
	}

	newData, err := json.Marshal(newObj)
	if err != nil {
		return fmt.Errorf("failed to marshal new data for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Patch(ctx, workloadClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
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

	// Get a JSON copy of the existing data
	oldData, err := json.Marshal(workloadCluster)
	if err != nil {
		return fmt.Errorf("failed to marshal old data for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	nowTime := metav1.NewTime(time.Now())

	// Build a JSON copy of the changed data
	newObj := workloadv1alpha1.WorkloadCluster{
		ObjectMeta: metav1.ObjectMeta{
			UID:             workloadCluster.UID,
			ResourceVersion: workloadCluster.ResourceVersion,
			Name:            workloadClusterName,
		},
		Spec: workloadv1alpha1.WorkloadClusterSpec{
			Unschedulable: true,
			EvictAfter:    &nowTime,
		},
	}

	// Get a JSON copy of the changed data
	newData, err := json.Marshal(newObj)
	if err != nil {
		return fmt.Errorf("failed to marshal new data for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for WorkloadCluster %s: %w", workloadClusterName, err)
	}

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Patch(ctx, workloadClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to update WorkloadCluster %s: %w", workloadClusterName, err)
	}

	fmt.Println(workloadClusterName, "draining")

	return nil

}
