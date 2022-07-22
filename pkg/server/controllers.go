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

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/certificates/rootcacertpublisher"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/serviceaccount"

	confighome "github.com/kcp-dev/kcp/config/home"
	confighomebucket "github.com/kcp-dev/kcp/config/homebucket"
	confighomeroot "github.com/kcp-dev/kcp/config/homeroot"
	configorganization "github.com/kcp-dev/kcp/config/organization"
	configteam "github.com/kcp-dev/kcp/config/team"
	configuniversal "github.com/kcp-dev/kcp/config/universal"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibindingdeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource"
	schedulinglocationstatus "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
	schedulingplacement "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/placement"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/bootstrap"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspaceshard"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacetype"
	workloadsapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
	workloadsapiexportcreate "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexportcreate"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/defaultplacement"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/heartbeat"
	workloadnamespace "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	workloadresource "github.com/kcp-dev/kcp/pkg/reconciler/workload/resource"
	virtualworkspaceurlscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/virtualworkspaceurls"
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

	discoverResourcesFn := func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += clusterName.Path()
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
		time.Duration(5)*time.Minute,
		corev1.FinalizerKubernetes,
	)

	s.AddPostStartHook("kcp-start-kube-namespace-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-start-kube-namespace-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(10, ctx.Done())
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

func (s *Server) installWorkspaceDeletionController(ctx context.Context, config *rest.Config) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-workspace-deletion-controller"))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}
	metadataClusterClient, err := metadata.NewForConfig(config)
	if err != nil {
		return err
	}
	discoverResourcesFn := func(clusterName logicalcluster.Name) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += clusterName.Path()
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(logicalClusterConfig)
		if err != nil {
			return nil, err
		}
		return discoveryClient.ServerPreferredResources()
	}

	workspaceDeletionController := clusterworkspacedeletion.NewController(
		kcpClusterClient,
		metadataClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		discoverResourcesFn,
	)

	s.AddPostStartHook("kcp-workspace-deletion-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-workspace-deletion-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go workspaceDeletionController.Start(ctx, 10)
		return nil
	})
	return nil
}

func (s *Server) installWorkloadResourceScheduler(ctx context.Context, config *rest.Config, ddsif *informer.DynamicDiscoverySharedInformerFactory) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-workload-resource-scheduler"))
	dynamicClusterClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	resourceScheduler, err := workloadresource.NewController(
		dynamicClusterClient,
		s.dynamicDiscoverySharedInformerFactory,
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-workload-resource-scheduler", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-namespace-scheduler: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go resourceScheduler.Start(ctx, 2)
		return nil
	})
	return nil
}

func (s *Server) installWorkspaceScheduler(ctx context.Context, config *rest.Config) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-workspace-scheduler"))

	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	workspaceController, err := clusterworkspace.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.rootKcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	var workspaceShardController *clusterworkspaceshard.Controller
	if s.options.Extra.ShardName == tenancyv1alpha1.RootShard {
		workspaceShardController, err = clusterworkspaceshard.NewController(
			kcpClusterClient,
			s.rootKcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		)
		if err != nil {
			return err
		}
	}

	workspaceTypeController, err := clusterworkspacetype.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceTypes(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
	)
	if err != nil {
		return err
	}

	organizationController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "organization"},
		configorganization.Bootstrap,
	)
	if err != nil {
		return err
	}

	teamController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "team"},
		configteam.Bootstrap,
	)
	if err != nil {
		return err
	}

	universalController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "universal"},
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
		if workspaceShardController != nil {
			go workspaceShardController.Start(ctx, 2)
		}
		go workspaceTypeController.Start(ctx, 2)
		go organizationController.Start(ctx, 2)
		go teamController.Start(ctx, 2)
		go universalController.Start(ctx, 2)

		return nil
	})
	return nil
}

func (s *Server) installHomeWorkspaces(ctx context.Context, config *rest.Config) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-home-workspaces"))

	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	homerootController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "homeroot"},
		confighomeroot.Bootstrap,
	)
	if err != nil {
		return err
	}

	homebucketController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "homebucket"},
		confighomebucket.Bootstrap,
	)
	if err != nil {
		return err
	}

	homeController, err := bootstrap.NewController(
		config,
		dynamicClusterClient,
		crdClusterClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "home"},
		confighome.Bootstrap,
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-home-workspaces", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-home-workspaces: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go homerootController.Start(ctx, 2)
		go homebucketController.Start(ctx, 2)
		go homeController.Start(ctx, 2)

		return nil
	})
	return nil
}

func (s *Server) installApiResourceController(ctx context.Context, config *rest.Config) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-api-resource-controller"))

	crdClusterClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return err
	}
	kcpClusterClient, err := kcpclient.NewForConfig(config)
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
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-api-resource-controller", func(hookContext genericapiserver.PostStartHookContext) error {
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

func (s *Server) installSyncTargetHeartbeatController(ctx context.Context, config *rest.Config) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-synctarget-heartbeat-controller"))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := heartbeat.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.options.Controllers.SyncTargetHeartbeat.HeartbeatThreshold,
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-install-cluster-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-cluster-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx)

		return nil
	})
	return nil

}

func (s *Server) installAPIBindingController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer, ddsif *informer.DynamicDiscoverySharedInformerFactory) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-apibinding-controller"))

	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}
	dynamicClusterClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	crdClusterClient, err := apiextensionsclient.NewForConfig(config)
	if err != nil {
		return err
	}

	metadataClient, err := metadata.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apibinding.NewController(
		crdClusterClient,
		kcpClusterClient,
		dynamicClusterClient,
		ddsif,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-apibinding-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return crdsSynced && exportsSynced && bindingsSynced, nil
		}); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-apibinding-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	apibindingDeletionController := apibindingdeletion.NewController(
		metadataClient,
		kcpClusterClient,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)

	if err := server.AddPostStartHook("kcp-install-apibinding-deletion-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-apibinding-deletion-controller: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go apibindingDeletionController.Start(goContext(hookContext), 10)

		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) installAPIExportController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), "kcp-apiexport-controller"))

	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	kubeClusterClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiexport.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		kubeClusterClient,
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kubeSharedInformerFactory.Core().V1().Secrets(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook("kcp-install-apiexport-controller", func(hookContext genericapiserver.PostStartHookContext) error {
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return crdsSynced && exportsSynced && bindingsSynced, nil
		}); err != nil {
			klog.Errorf("failed to finish post-start-hook kcp-install-apiexport-controller: %v", err)
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

func (s *Server) installSchedulingLocationStatusController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-scheduling-location-status-controller"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))

	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulinglocationstatus.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.kcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installDefaultPlacementController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-workload-default-placement"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := defaultplacement.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installWorkloadNamespaceScheduler(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-workload-namespace-scheduler"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kubeClusterClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadnamespace.NewController(
		kubeClusterClient,
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.kcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installSchedulingPlacementController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-scheduling-placement-controller"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulingplacement.NewController(
		kcpClusterClient,
		s.kubeSharedInformerFactory.Core().V1().Namespaces(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installWorkloadsAPIExportController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-workloads-apiexport-controller"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexport.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.kcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installWorkloadsAPIExportCreateController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-workloads-apiexport-create-controller"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexportcreate.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.kcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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

func (s *Server) installVirtualWorkspaceURLsController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-virtualworkspace-urls-controller"
	config = kcpclienthelper.NewClusterConfig(rest.AddUserAgent(rest.CopyConfig(config), controllerName))
	kcpClusterClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return err
	}

	c := virtualworkspaceurlscontroller.NewController(
		kcpClusterClient,
		s.kcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(controllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			klog.Errorf("failed to finish post-start-hook %s: %v", controllerName, err)
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
