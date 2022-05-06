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
		return fmt.Errorf("failed to get workloadcluster %s: %w", workloadClusterName, err)
	}

	workloadCluster.Spec.Unschedulable = cordon

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Update(ctx, workloadCluster, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update workloadcluster %s: %w", workloadClusterName, err)
	}

	return nil
}

// Uncordon the workload cluster and mark it as schedulable
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

	nowTime := metav1.NewTime(time.Now())
	workloadCluster.Spec.EvictAfter = &nowTime

	_, err = kcpClient.WorkloadV1alpha1().WorkloadClusters().Update(ctx, workloadCluster, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update workloadcluster %s: %w", workloadClusterName, err)
	}

	return nil

}
