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
	"fmt"
	"io/ioutil"
	_ "net/http/pprof"
	"os"
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
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/certificates/rootcacertpublisher"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/serviceaccount"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	configorganization "github.com/kcp-dev/kcp/config/organization"
	configuniversal "github.com/kcp-dev/kcp/config/universal"
	apiresourceapi "github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/apis"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	"github.com/kcp-dev/kcp/pkg/apis/workload"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/gvk"
	"github.com/kcp-dev/kcp/pkg/reconciler/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	clusterapiimporter "github.com/kcp-dev/kcp/pkg/reconciler/cluster/apiimporter"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster/syncer"
	"github.com/kcp-dev/kcp/pkg/reconciler/clusterworkspacetypebootstrap"
	kcpnamespace "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/workspaceshard"
)

func (s *Server) installClusterRoleAggregationController(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kube-cluster-role-aggregation-controller")
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
	config = rest.AddUserAgent(rest.CopyConfig(config), "kube-namespace-controller")
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

func (s *Server) installKubeServiceAccountController(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kube-service-account-controller")
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := serviceaccountcontroller.NewServiceAccountsController(
		s.kubeSharedInformerFactory.Core().V1().ServiceAccounts(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		kubeClient,
		serviceaccountcontroller.DefaultServiceAccountsControllerOptions(),
	)
	if err != nil {
		return fmt.Errorf("error creating ServiceAccount controller: %w", err)
	}

	s.AddPostStartHook("kcp-start-kube-service-account-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-start-kube-service-account-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(ctx, 1)
		return nil
	})

	return nil
}

func (s *Server) installKubeServiceAccountTokenController(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kube-service-account-token-controller")
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	serviceAccountKeyFile := s.options.Controllers.SAController.ServiceAccountKeyFile
	if len(serviceAccountKeyFile) == 0 {
		return fmt.Errorf("service account controller requires a private key")
	}
	privateKey, err := keyutil.PrivateKeyFromFile(serviceAccountKeyFile)
	if err != nil {
		return fmt.Errorf("error reading key for service account token controller: %w", err)
	}

	var rootCA []byte
	rootCAFile := s.options.Controllers.SAController.RootCAFile
	if rootCAFile != "" {
		if rootCA, err = readCA(rootCAFile); err != nil {
			return fmt.Errorf("error parsing root-ca-file at %s: %w", rootCAFile, err)
		}
	} else {
		rootCA = config.CAData
	}

	tokenGenerator, err := serviceaccount.JWTTokenGenerator(serviceaccount.LegacyIssuer, privateKey)
	if err != nil {
		return fmt.Errorf("failed to build token generator: %w", err)
	}
	controller, err := serviceaccountcontroller.NewTokensController(
		s.kubeSharedInformerFactory.Core().V1().ServiceAccounts(),
		s.kubeSharedInformerFactory.Core().V1().Secrets(),
		kubeClient,
		serviceaccountcontroller.TokensControllerOptions{
			TokenGenerator: tokenGenerator,
			RootCA:         rootCA,
		},
	)
	if err != nil {
		return fmt.Errorf("error creating service account controller: %w", err)
	}

	s.AddPostStartHook("kcp-start-kube-service-account-token-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-start-kube-service-account-token-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go controller.Run(int(s.options.Controllers.SAController.ConcurrentSATokenSyncs), ctx.Done())

		return nil
	})

	return nil
}

func (s *Server) installRootCAConfigMapController(ctx context.Context, config *rest.Config) error {
	rootCAConfigMapControllerName := "kube-root-ca-configmap-controller"

	config = rest.AddUserAgent(rest.CopyConfig(config), rootCAConfigMapControllerName)
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	// TODO(jmprusi): We should make the CA loading dynamic when the file changes on disk.
	caDataPath := s.options.Controllers.SAController.RootCAFile
	if caDataPath == "" {
		caDataPath = s.options.GenericControlPlane.SecureServing.SecureServingOptions.ServerCert.CertKey.CertFile
	}

	caData, err := os.ReadFile(caDataPath)
	if err != nil {
		return fmt.Errorf("error parsing root-ca-file at %s: %w", caDataPath, err)
	}

	c, err := rootcacertpublisher.NewPublisher(
		s.kubeSharedInformerFactory.Core().V1().ConfigMaps(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		kubeClient,
		caData,
	)
	if err != nil {
		return fmt.Errorf("error creating %s controller: %w", rootCAConfigMapControllerName, err)
	}

	s.AddPostStartHook("kcp-start-"+rootCAConfigMapControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", rootCAConfigMapControllerName, err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(2, ctx.Done())
		return nil
	})

	return nil
}

func readCA(file string) ([]byte, error) {
	rootCA, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if _, err := certutil.ParseCertsPEM(rootCA); err != nil {
		return nil, err
	}

	return rootCA, err
}

func (s *Server) installNamespaceScheduler(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-namespace-scheduler")
	kubeClient, err := kubernetes.NewClusterForConfig(config)
	if err != nil {
		return err
	}
	dynamicClusterClient, err := dynamic.NewClusterForConfig(config)
	if err != nil {
		return err
	}
	dynamicClient := dynamicClusterClient

	// TODO(ncdc): I dont' think this is used anywhere?
	gvkTrans := gvk.NewGVKTranslator(config)

	namespaceScheduler := kcpnamespace.NewController(
		dynamicClient,
		kubeClient.DiscoveryClient,
		kubeClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.kcpSharedInformerFactory.Workload().V1alpha1().WorkloadClusters(),
		s.kcpSharedInformerFactory.Workload().V1alpha1().WorkloadClusters().Lister(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kubeSharedInformerFactory.Core().V1().Namespaces().Lister(),
		gvkTrans,
		s.options.Extra.DiscoveryPollInterval,
	)

	s.AddPostStartHook("kcp-install-namespace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-namespace-scheduler: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go namespaceScheduler.Start(ctx, 2)
		return nil
	})
	return nil
}

func (s *Server) installWorkspaceScheduler(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-workspace-scheduler")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := dynamic.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	workspaceController, err := workspace.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.rootKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	workspaceShardController, err := workspaceshard.NewController(
		kcpClusterClient.Cluster(helper.RootCluster),
		s.rootKubeSharedInformerFactory.Core().V1().Secrets(),
		s.rootKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceShards(),
	)
	if err != nil {
		return err
	}

	organizationController, err := clusterworkspacetypebootstrap.NewController(
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		"Organization",
		configorganization.Bootstrap,
	)
	if err != nil {
		return err
	}

	universalController, err := clusterworkspacetypebootstrap.NewController(
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		"Universal",
		configuniversal.Bootstrap,
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-workspace-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-workspace-scheduler: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go workspaceController.Start(ctx, 2)
		go workspaceShardController.Start(ctx, 2)
		go organizationController.Start(ctx, 2)
		go universalController.Start(ctx, 2)

		return nil
	})
	return nil
}

func (s *Server) installApiImportController(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-api-import-controller")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	c, err := clusterapiimporter.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Workload().V1alpha1().WorkloadClusters(),
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.options.Controllers.ApiImporter.ResourcesToSync,
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-api-importer-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-api-importer-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx)

		return nil
	})
	return nil
}

func (s *Server) installApiResourceController(ctx context.Context, config *rest.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-api-resource-controller")
	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiresource.NewController(
		crdClusterClient,
		kcpClusterClient,
		s.options.Controllers.ApiResource.AutoPublishAPIs,
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)

	s.AddPostStartHook("kcp-install-api-resource-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		// HACK HACK HACK
		// TODO(sttts): these CRDs can go away when when we don't need a CRD in some workspace for "*" informers to work
		err = configcrds.Create(ctx, crdClusterClient.Cluster(genericcontrolplane.LocalAdminCluster).ApiextensionsV1().CustomResourceDefinitions(),
			metav1.GroupResource{Group: workload.GroupName, Resource: "workloadclusters"},
			metav1.GroupResource{Group: apiresourceapi.GroupName, Resource: "apiresourceimports"},
			metav1.GroupResource{Group: apiresourceapi.GroupName, Resource: "negotiatedapiresources"},
			metav1.GroupResource{Group: apis.GroupName, Resource: "apiresourceschemas"},
			metav1.GroupResource{Group: apis.GroupName, Resource: "apiexports"},
			metav1.GroupResource{Group: apis.GroupName, Resource: "apibindings"},
		)
		if err != nil {
			return err
		}

		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-api-resource-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx, s.options.Controllers.ApiResource.NumThreads)

		return nil
	})
	return nil
}

func (s *Server) installSyncerController(ctx context.Context, config *rest.Config, pclusterKubeconfig *clientcmdapi.Config) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-syncer-controller")
	manager := s.options.Controllers.Syncer.CreateSyncerManager()
	if manager == nil {
		klog.Info("syncer not enabled. To enable, supply --pull-mode or --push-mode")
		return nil
	}

	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}
	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	c, err := syncer.NewController(
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Workload().V1alpha1().WorkloadClusters(),
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		pclusterKubeconfig,
		s.options.Controllers.Syncer.ResourcesToSync,
		manager,
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-syncer-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-syncer-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx)

		return nil
	})
	return nil
}

func (s *Server) installAPIBindingController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.AddUserAgent(rest.CopyConfig(config), "kcp-apibinding-controller")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(config)
	if err != nil {
		return err
	}

	c, err := apibinding.NewController(
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-apibinding-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-apibinding-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

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
