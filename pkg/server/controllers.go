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
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"

	"github.com/kcp-dev/kcp/config"
	"github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/cluster"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/gvk"
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

	s.AddPostStartHook("start-kube-cluster-role-aggregation-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		go c.Run(5, ctx.Done())
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

	s.AddPostStartHook("start-kube-namespace-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			return err
		}

		go c.Run(2, ctx.Done())
		return nil
	})

	return nil
}

func (s *Server) installNamespaceScheduler(ctx context.Context, workspaceLister tenancylisters.WorkspaceLister, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
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

	if err := server.AddPostStartHook("install-namespace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			return err
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
	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return err
	}

	kcpClient, err := kcpclient.NewClusterForConfig(adminConfig)
	if err != nil {
		return err
	}

	workspaceController, err := workspace.NewController(
		kcpClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	workspaceShardController, err := workspaceshard.NewController(
		kcpClient,
		s.kubeSharedInformerFactory.Core().V1().Secrets(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("install-workspace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			return err
		}

		go workspaceController.Start(ctx, 2)
		go workspaceShardController.Start(ctx, 2)

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installClusterController(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	for _, cluster := range kubeconfig.Clusters {
		hostURL, err := url.Parse(cluster.Server)
		if err != nil {
			return err
		}
		hostURL.Host = server.ExternalAddress
		cluster.Server = hostURL.String()
	}

	c := s.options.Controllers.Cluster.Complete(*kubeconfig, s.kcpSharedInformerFactory, s.apiextensionsSharedInformerFactory)
	cluster, apiresource, err := c.New()
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("install-cluster-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			return err
		}

		cluster, err := cluster.Prepare()
		if err != nil {
			return err
		}
		go cluster.Start(goContext(hookContext))
		go apiresource.Start(goContext(hookContext), c.NumThreads)

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) bootstrapCRDs(ctx context.Context, crdClient apiextensionsv1client.CustomResourceDefinitionInterface) error {
	requiredCrds := []metav1.GroupResource{
		{Group: tenancyapi.GroupName, Resource: "workspaces"},
		{Group: tenancyapi.GroupName, Resource: "workspaceshards"},
		{Group: apiresource.GroupName, Resource: "apiresourceimports"},
		{Group: apiresource.GroupName, Resource: "negotiatedapiresources"},
		{Group: cluster.GroupName, Resource: "clusters"},
	}

	if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
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
