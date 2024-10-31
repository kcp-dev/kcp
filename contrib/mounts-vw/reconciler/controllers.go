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
	mountskubecluster "github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/mounts/kubecluster"
	mountsvcluster "github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/mounts/vcluster"
	targetskubecluster "github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/targets/kubecluster"
	targetsvcluster "github.com/kcp-dev/kcp/contrib/mounts-vw/reconciler/targets/vcluster"
)

func (s *shardManager) installTargetKubeClustersController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", targetskubecluster.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, targetskubecluster.ControllerName)
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

	c, err := targetskubecluster.NewController(
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

func (s *shardManager) installTargetVClustersController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", targetsvcluster.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, targetsvcluster.ControllerName)
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

	c, err := targetsvcluster.NewController(
		kcpClusterClient,
		mountsClusterClient,
		kubeClusterClient,
		dynamicClusterClient,
		s.mountsSharedInformerFactory.Targets().V1alpha1().TargetVClusters(),
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

func (s *shardManager) installKubeClusterMountsController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", mountskubecluster.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, mountskubecluster.ControllerName)
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

	c, err := mountskubecluster.NewController(
		kcpClusterClient,
		mountsClusterClient,
		kubeClusterClient,
		dynamicClusterClient,
		s.mountsSharedInformerFactory.Mounts().V1alpha1().KubeClusters(),
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

func (s *shardManager) installVClusterMountsController(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("shard", s.name, "controller", mountsvcluster.ControllerName)

	config := rest.CopyConfig(&s.shardClientConfig)
	config = rest.AddUserAgent(config, mountsvcluster.ControllerName)
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

	c, err := mountsvcluster.NewController(
		kcpClusterClient,
		mountsClusterClient,
		kubeClusterClient,
		dynamicClusterClient,
		s.mountsSharedInformerFactory.Mounts().V1alpha1().VClusters(),
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
