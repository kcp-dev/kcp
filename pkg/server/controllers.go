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
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"

	"github.com/kcp-dev/kcp/config"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/gvk"
	kcpnamespace "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspaceshard"
)

func (s *Server) installClusterRoleAggregationController(config *rest.Config) error {
	kubeClient, err := kubernetes.NewClusterForConfig(config)
	if err != nil {
		return err
	}
	adminScope := "admin"
	versionedInformer := coreexternalversions.NewSharedInformerFactory(kubeClient.Cluster(adminScope), resyncPeriod)

	c := clusterroleaggregation.NewClusterRoleAggregation(
		versionedInformer.Rbac().V1().ClusterRoles(),
		kubeClient.Cluster(adminScope).RbacV1())

	s.AddPostStartHook("start-kube-cluster-role-aggregation-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		go c.Run(5, hookContext.StopCh)
		return nil
	})

	return nil
}

func (s *Server) installKubeNamespaceController(config *rest.Config) error {
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
		go c.Run(2, hookContext.StopCh)
		return nil
	})

	return nil
}

func (s *Server) installNamespaceScheduler(ctx context.Context, clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	kubeconfig := clientConfig.DeepCopy()
	adminConfig, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return err
	}

	kubeClient := kubernetes.NewForConfigOrDie(adminConfig)
	disco := discovery.NewDiscoveryClientForConfigOrDie(adminConfig)
	dynClient := dynamic.NewForConfigOrDie(adminConfig)

	gvkTrans := gvk.NewGVKTranslator(adminConfig)

	namespaceScheduler := kcpnamespace.NewController(
		dynClient,
		disco,
		s.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters(),
		s.kcpSharedInformerFactory.Cluster().V1alpha1().Clusters().Lister(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces().Lister(),
		kubeClient.CoreV1().Namespaces(),
		gvkTrans,
	)

	if err := server.AddPostStartHook("install-namespace-scheduler", func(context genericapiserver.PostStartHookContext) error {
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

	if err := server.AddPostStartHook("install-workspace-scheduler", func(context genericapiserver.PostStartHookContext) error {
		if err := s.waitForCRDServer(context.StopCh); err != nil {
			return err
		}

		// Register CRDs in both the admin and user logical clusters
		requiredCrds := []metav1.GroupKind{
			{Group: tenancyapi.GroupName, Kind: "workspaces"},
			{Group: tenancyapi.GroupName, Kind: "workspaceshards"},
		}
		crdClient := apiextensionsv1client.NewForConfigOrDie(adminConfig).CustomResourceDefinitions()
		if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
			return err
		}

		ctx := goContext(context)
		go workspaceController.Start(ctx, 2)
		go workspaceShardController.Start(ctx, 2)

		return nil
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) installClusterController(clientConfig clientcmdapi.Config, server *genericapiserver.GenericAPIServer) error {
	if err := s.cfg.ClusterControllerOptions.Validate(); err != nil {
		return err
	}

	kubeconfig := clientConfig.DeepCopy()
	for _, cluster := range kubeconfig.Clusters {
		hostURL, err := url.Parse(cluster.Server)
		if err != nil {
			return err
		}
		hostURL.Host = server.ExternalAddress
		cluster.Server = hostURL.String()
	}

	if err := server.AddPostStartHook("install-cluster-controller", func(context genericapiserver.PostStartHookContext) error {
		if err := s.waitForCRDServer(context.StopCh); err != nil {
			return err
		}

		ctx := goContext(context)
		return s.cfg.ClusterControllerOptions.Complete(*kubeconfig, s.kcpSharedInformerFactory, s.apiextensionsSharedInformerFactory).Start(ctx)
	}); err != nil {
		return err
	}
	return nil
}

func (s *Server) waitForCRDServer(stop <-chan struct{}) error {
	// Wait for the CRD server to be ready before doing things such as starting the shared informer
	// factory. Otherwise, informer list calls may go into backoff (before the CRDs are ready) and
	// take ~10 seconds to succeed.
	select {
	case <-stop:
		return errors.New("timed out waiting for CRD server to be ready")
	case <-s.crdServerReady:
		return nil
	}
}
