/*
Copyright 2023 The KCP Authors.

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

package reconciler

import (
	"context"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	mountsclientset "github.com/kcp-dev/kcp/contrib/mounts-vw/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/mounts"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/targets"
)

func (s *shardManager) installTargetKubeClustersController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", targets.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, targets.ControllerName)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	mountsClusterClient, err := mountsclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := targets.NewController(
		kcpClusterClient,
		mountsClusterClient,
		kubeClusterClient,
		dynamicClusterClient,
		s.mountsSharedInformerFactory.Targets().V1alpha1().TargetKubeClusters(),
		s.store,
		s.virtualWorkspaceURL,
	)
	if err != nil {
		return err
	}

	go func() {
		// Wait for shared informer factories to by synced.
		<-s.syncedCh
		ctx = klog.NewContext(ctx, logger)
		logger.Info("starting controller")
		c.Start(ctx, 2)
	}()

	return nil
}

func (s *shardManager) installMountsController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", mounts.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, mounts.ControllerName)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	mountsClusterClient, err := mountsclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := mounts.NewController(
		kcpClusterClient,
		mountsClusterClient,
		kubeClusterClient,
		dynamicClusterClient,
		s.mountsSharedInformerFactory.Mounts().V1alpha1().KubeClusters(),
		s.store,
	)
	if err != nil {
		return err
	}

	go func() {
		// Wait for shared informer factories to by synced.
		<-s.syncedCh
		ctx = klog.NewContext(ctx, logger)
		logger.Info("starting controller")
		c.Start(ctx, 2)
	}()

	return nil
}
