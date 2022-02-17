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

package server

import (
	"context"
	"errors"
	_ "net/http/pprof"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/gvk"
	"github.com/kcp-dev/kcp/pkg/reconciler/clusterworkspacetype_organization"
	kcpnamespace "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspaceshard"
)

func (s *Server) installClusterRoleAggregationController(ctx context.Context, config *rest.Config) error {
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	c := clusterroleaggregation.NewClusterRoleAggregation(
		s.kubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		kubeClient.RbacV1())

	s.AddPostStartHook("kcp-start-kube-cluster-role-aggregation-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		go c.Run(ctx, 5)
		return nil
	})

	return nil
}

func (s *Server) installKubeNamespaceController(ctx context.Context, config *rest.Config) error {
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	metadata, err := metadata.NewForConfig(config)
	if err != nil {
		return err
	}

	discoverResourcesFn := func(clusterName string) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += "/clusters/" + clusterName
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(logicalClusterConfig)
		if err != nil {
			return nil, err
		}
		return discoveryClient.ServerPreferredNamespacedResources()
	}

	// We have to construct this outside of / before any post-start hooks are invoked, because
	// the constructor sets up event handlers on shared informers, which instructs the factory
	// which informers need to be started. The shared informer factories are started in their
	// own post-start hook.
	c := namespace.NewNamespaceController(
		kubeClient,
		metadata,
		discoverResourcesFn,
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		time.Duration(30)*time.Second,
		corev1.FinalizerKubernetes,
	)

	s.AddPostStartHook("kcp-start-kube-namespace-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-start-kube-namespace-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(2, ctx.Done())
		return nil
	})

	return nil
}

func (s *Server) installNamespaceScheduler(ctx context.Context, workspaceLister tenancylisters.ClusterWorkspaceLister, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeClient, err := kubernetes.NewClusterForConfig(server.LoopbackClientConfig)
	if err != nil {
		return err
	}
	dynamicClusterClient, err := dynamic.NewClusterForConfig(server.LoopbackClientConfig)
	if err != nil {
		return err
	}
	dynamicClient := dynamicClusterClient

	// TODO(ncdc): I dont' think this is used anywhere?
	gvkTrans := gvk.NewGVKTranslator(server.LoopbackClientConfig)

	namespaceScheduler := kcpnamespace.NewController(
		workspaceLister,
		dynamicClient,
		kubeClient.DiscoveryClient,
		s.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters(),
		s.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters().Lister(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces().Lister(),
		kubeClient,
		gvkTrans,
		s.options.Extra.DiscoveryPollInterval,
	)

	if err := server.AddPostStartHook("kcp-install-namespace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-namespace-scheduler: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go namespaceScheduler.Start(ctx, 2)
		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installWorkspaceScheduler(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, "system:admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return err
	}

	kcpClusterClient, err := kcpclient.NewClusterForConfig(adminConfig)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(adminConfig)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := dynamic.NewClusterForConfig(adminConfig)
	if err != nil {
		return err
	}

	workspaceController, err := workspace.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	workspaceShardController, err := workspaceshard.NewController(
		kcpClusterClient,
		s.kubeSharedInformerFactory.Core().V1().Secrets(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	organizationController, err := clusterworkspacetype_organization.NewController(
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-workspace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-workspace-scheduler: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go workspaceController.Start(ctx, 2)
		go workspaceShardController.Start(ctx, 2)
		go organizationController.Start(ctx, 2)

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installApiImportController(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	for _, cluster := range kubeconfig.Clusters {
		hostURL, err := url.Parse(cluster.Server)
		if err != nil {
			return err
		}
		hostURL.Host = server.ExternalAddress
		cluster.Server = hostURL.String()
	}

	c := s.options.Controllers.ApiImporter.Complete(*kubeconfig, s.kcpSharedInformerFactory, s.apiextensionsSharedInformerFactory)
	apiimporter, err := c.New()
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-api-importer-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-api-importer-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go apiimporter.Start(goContext(hookContext))

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installApiResourceController(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	for _, cluster := range kubeconfig.Clusters {
		hostURL, err := url.Parse(cluster.Server)
		if err != nil {
			return err
		}
		hostURL.Host = server.ExternalAddress
		cluster.Server = hostURL.String()
	}

	c := s.options.Controllers.ApiResource.Complete(*kubeconfig, s.kcpSharedInformerFactory, s.apiextensionsSharedInformerFactory)
	apiresource, err := c.New()
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-api-resource-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-api-resource-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go apiresource.Start(goContext(hookContext), c.NumThreads)

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installSyncerController(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	for _, cluster := range kubeconfig.Clusters {
		hostURL, err := url.Parse(cluster.Server)
		if err != nil {
			return err
		}
		hostURL.Host = server.ExternalAddress
		cluster.Server = hostURL.String()
	}

	c := s.options.Controllers.Syncer.Complete(
		*kubeconfig,
		s.kcpSharedInformerFactory,
		s.apiextensionsSharedInformerFactory,
		s.options.Controllers.ApiImporter.ResourcesToSync,
	)
	syncer, err := c.New()
	if err != nil {
		return err
	}
	if syncer == nil {
		klog.Info("syncer not enabled. To enable, supply --pull-mode or --push-mode")
		return nil
	}

	if err := server.AddPostStartHook("kcp-install-syncer-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-syncer-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		syncer, err := syncer.Prepare()
		if err != nil {
			return err
		}

		go syncer.Start(goContext(hookContext))

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) waitForSync(stop <-chan struct{}) error {
	// Wait for shared informer factories to by synced.
	// factory. Otherwise, informer list calls may go into backoff (before the CRDs are ready) and
	// take ~10 seconds to succeed.
	select {
	case <-stop:
		return errors.New("timed out waiting for informers to sync")
	case <-s.syncedCh:
		return nil
	}
}
