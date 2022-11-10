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
	_ "net/http/pprof"
	"os"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/certificates/rootcacertpublisher"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/serviceaccount"

	configuniversal "github.com/kcp-dev/kcp/config/universal"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibindingdeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/crdcleanup"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/extraannotationsync"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/permissionclaimlabel"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/garbagecollector"
	"github.com/kcp-dev/kcp/pkg/reconciler/kubequota"
	schedulinglocationstatus "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
	schedulingplacement "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/placement"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/bootstrap"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspaceshard"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacetype"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization"
	workloadsapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
	workloadsapiexportcreate "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexportcreate"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/heartbeat"
	workloadnamespace "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	workloadplacement "github.com/kcp-dev/kcp/pkg/reconciler/workload/placement"
	workloadresource "github.com/kcp-dev/kcp/pkg/reconciler/workload/resource"
	synctargetcontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/synctarget"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/synctargetexports"
	initializingworkspacesbuilder "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/builder"
)

func postStartHookName(controllerName string) string {
	return fmt.Sprintf("kcp-start-%s", controllerName)
}

func (s *Server) installClusterRoleAggregationController(ctx context.Context, config *rest.Config) error {
	controllerName := "kube-cluster-role-aggregation-controller"
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	c := clusterroleaggregation.NewClusterRoleAggregation(
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		kubeClient.RbacV1())

	return s.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		go c.Run(ctx, 5)
		return nil
	})
}

func (s *Server) installKubeNamespaceController(ctx context.Context, config *rest.Config) error {
	controllerName := "kube-namespace-controller"
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	metadata, err := kcpmetadata.NewForConfig(config)
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
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		time.Duration(5)*time.Minute,
		corev1.FinalizerKubernetes,
	)

	return s.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(10, ctx.Done())
		return nil
	})
}

func (s *Server) installKubeServiceAccountController(ctx context.Context, config *rest.Config) error {
	controllerName := "kube-service-account-controller"
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := serviceaccountcontroller.NewServiceAccountsController(
		s.KubeSharedInformerFactory.Core().V1().ServiceAccounts(),
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		kubeClient,
		serviceaccountcontroller.DefaultServiceAccountsControllerOptions(),
	)
	if err != nil {
		return fmt.Errorf("error creating ServiceAccount controller: %w", err)
	}

	return s.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(ctx, 1)
		return nil
	})
}

func (s *Server) installKubeServiceAccountTokenController(ctx context.Context, config *rest.Config) error {
	controllerName := "kube-service-account-token-controller"
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	serviceAccountKeyFile := s.Options.Controllers.SAController.ServiceAccountKeyFile
	if len(serviceAccountKeyFile) == 0 {
		return fmt.Errorf("service account controller requires a private key")
	}
	privateKey, err := keyutil.PrivateKeyFromFile(serviceAccountKeyFile)
	if err != nil {
		return fmt.Errorf("error reading key for service account token controller: %w", err)
	}

	var rootCA []byte
	rootCAFile := s.Options.Controllers.SAController.RootCAFile
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
		s.KubeSharedInformerFactory.Core().V1().ServiceAccounts(),
		s.KubeSharedInformerFactory.Core().V1().Secrets(),
		kubeClient,
		serviceaccountcontroller.TokensControllerOptions{
			TokenGenerator: tokenGenerator,
			RootCA:         rootCA,
			AutoGenerate:   true,
		},
	)
	if err != nil {
		return fmt.Errorf("error creating service account controller: %w", err)
	}

	return s.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go controller.Run(int(s.Options.Controllers.SAController.ConcurrentSATokenSyncs), ctx.Done())

		return nil
	})
}

func (s *Server) installRootCAConfigMapController(ctx context.Context, config *rest.Config) error {
	controllerName := "kube-root-ca-configmap-controller"
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	// TODO(jmprusi): We should make the CA loading dynamic when the file changes on disk.
	caDataPath := s.Options.Controllers.SAController.RootCAFile
	if caDataPath == "" {
		caDataPath = s.Options.GenericControlPlane.SecureServing.SecureServingOptions.ServerCert.CertKey.CertFile
	}

	caData, err := os.ReadFile(caDataPath)
	if err != nil {
		return fmt.Errorf("error parsing root-ca-file at %s: %w", caDataPath, err)
	}

	c, err := rootcacertpublisher.NewPublisher(
		s.KubeSharedInformerFactory.Core().V1().ConfigMaps(),
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		kubeClient,
		caData,
	)
	if err != nil {
		return fmt.Errorf("error creating %s controller: %w", controllerName, err)
	}

	return s.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Run(ctx, 2)
		return nil
	})
}

func readCA(file string) ([]byte, error) {
	rootCA, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if _, err := certutil.ParseCertsPEM(rootCA); err != nil {
		return nil, err
	}

	return rootCA, err
}

func (s *Server) installWorkspaceDeletionController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, clusterworkspacedeletion.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	metadataClusterClient, err := kcpmetadata.NewForConfig(config)
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
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	workspaceDeletionController := clusterworkspacedeletion.NewController(
		kubeClusterClient,
		kcpClusterClient,
		metadataClusterClient,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		discoverResourcesFn,
	)

	return s.AddPostStartHook(postStartHookName(clusterworkspacedeletion.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(clusterworkspacedeletion.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go workspaceDeletionController.Start(ctx, 10)
		return nil
	})
}

func (s *Server) installWorkloadResourceScheduler(ctx context.Context, config *rest.Config, ddsif *informer.DynamicDiscoverySharedInformerFactory) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadresource.ControllerName)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return err
	}

	resourceScheduler, err := workloadresource.NewController(
		dynamicClusterClient,
		s.DynamicDiscoverySharedInformerFactory,
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(workloadresource.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadresource.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go resourceScheduler.Start(ctx, 2)
		return nil
	})
}

func (s *Server) installWorkspaceScheduler(ctx context.Context, config *rest.Config, logicalClusterAdminConfig *rest.Config) error {
	// NOTE: keep `config` unaltered so there isn't cross-use between controllers installed here.
	clusterWorkspaceConfig := rest.CopyConfig(config)
	clusterWorkspaceConfig = rest.AddUserAgent(clusterWorkspaceConfig, clusterworkspace.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(clusterWorkspaceConfig)
	if err != nil {
		return err
	}
	kubeClusterClient, err := kcpclientset.NewForConfig(clusterWorkspaceConfig)
	if err != nil {
		return err
	}

	workspaceController, err := clusterworkspace.NewController(
		kcpClusterClient,
		kubeClusterClient,
		logicalClusterAdminConfig,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.KcpSharedInformerFactory.Tenancy().V1beta1().Workspaces(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ThisWorkspaces(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(clusterworkspace.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(clusterworkspace.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go workspaceController.Start(ctx, 2)
		return nil
	}); err != nil {
		return err
	}

	clusterShardConfig := rest.CopyConfig(config)
	clusterShardConfig = rest.AddUserAgent(clusterShardConfig, clusterworkspaceshard.ControllerName)
	kcpClusterClient, err = kcpclientset.NewForConfig(clusterShardConfig)
	if err != nil {
		return err
	}

	var workspaceShardController *clusterworkspaceshard.Controller
	if s.Options.Extra.ShardName == tenancyv1alpha1.RootShard {
		workspaceShardController, err = clusterworkspaceshard.NewController(
			kcpClusterClient,
			s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		)
		if err != nil {
			return err
		}
	}
	if workspaceShardController != nil {
		if err := s.AddPostStartHook(postStartHookName(clusterworkspaceshard.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
			logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(clusterworkspaceshard.ControllerName))
			if err := s.waitForSync(hookContext.StopCh); err != nil {
				logger.Error(err, "failed to finish post-start-hook")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			go workspaceShardController.Start(ctx, 2)
			return nil
		}); err != nil {
			return err
		}

	}

	workspaceTypeConfig := rest.CopyConfig(config)
	workspaceTypeConfig = rest.AddUserAgent(workspaceTypeConfig, clusterworkspacetype.ControllerName)
	kcpClusterClient, err = kcpclientset.NewForConfig(workspaceTypeConfig)
	if err != nil {
		return err
	}

	workspaceTypeController, err := clusterworkspacetype.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceTypes(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(clusterworkspacetype.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(clusterworkspacetype.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go workspaceTypeController.Start(ctx, 2)
		return nil
	}); err != nil {
		return err
	}

	bootstrapConfig := rest.CopyConfig(config)
	universalControllerName := fmt.Sprintf("%s-%s", bootstrap.ControllerNameBase, "universal")
	bootstrapConfig = rest.AddUserAgent(bootstrapConfig, universalControllerName)
	bootstrapConfig.Impersonate.UserName = kcpBootstrapperUserName
	bootstrapConfig.Impersonate.Groups = []string{bootstrappolicy.SystemKcpWorkspaceBootstrapper}

	dynamicClusterClient, err := kcpdynamic.NewForConfig(bootstrapConfig)
	if err != nil {
		return err
	}

	bootstrapKcpClusterClient, err := kcpclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return err
	}

	universalController, err := bootstrap.NewController(
		dynamicClusterClient,
		bootstrapKcpClusterClient,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: "root", Name: "universal"},
		configuniversal.Bootstrap,
		sets.NewString(s.Options.Extra.BatteriesIncluded...),
	)
	if err != nil {
		return err
	}
	return s.AddPostStartHook(postStartHookName(universalControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(universalControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go universalController.Start(ctx, 2)
		return nil
	})
}

func (s *Server) installApiResourceController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiresource.ControllerName)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiresource.NewController(
		crdClusterClient,
		kcpClusterClient,
		s.Options.Controllers.ApiResource.AutoPublishAPIs,
		s.KcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		s.KcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(apiresource.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apiresource.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx, s.Options.Controllers.ApiResource.NumThreads)

		return nil
	})
}

func (s *Server) installSyncTargetHeartbeatController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, heartbeat.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := heartbeat.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		s.Options.Controllers.SyncTargetHeartbeat.HeartbeatThreshold,
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(heartbeat.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(heartbeat.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(ctx)

		return nil
	})
}

func (s *Server) installAPIBindingController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer, ddsif *informer.DynamicDiscoverySharedInformerFactory) error {
	// NOTE: keep `config` unaltered so there isn't cross-use between controllers installed here.
	apiBindingConfig := rest.CopyConfig(config)
	apiBindingConfig = rest.AddUserAgent(apiBindingConfig, apibinding.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(apiBindingConfig)
	if err != nil {
		return err
	}
	dynamicClusterClient, err := kcpdynamic.NewForConfig(apiBindingConfig)
	if err != nil {
		return err
	}

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(apiBindingConfig)
	if err != nil {
		return err
	}

	c, err := apibinding.NewController(
		crdClusterClient,
		kcpClusterClient,
		dynamicClusterClient,
		ddsif,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(postStartHookName(apibinding.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apibinding.ControllerName))
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return crdsSynced && exportsSynced && bindingsSynced, nil
		}); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	permissionClaimLabelConfig := rest.CopyConfig(config)
	permissionClaimLabelConfig = rest.AddUserAgent(permissionClaimLabelConfig, permissionclaimlabel.ControllerName)

	kcpClusterClient, err = kcpclientset.NewForConfig(permissionClaimLabelConfig)
	if err != nil {
		return err
	}
	dynamicClusterClient, err = kcpdynamic.NewForConfig(permissionClaimLabelConfig)
	if err != nil {
		return err
	}

	permissionClaimLabelController, err := permissionclaimlabel.NewController(
		kcpClusterClient,
		dynamicClusterClient,
		ddsif,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(postStartHookName(permissionclaimlabel.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(permissionclaimlabel.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go permissionClaimLabelController.Start(goContext(hookContext), 5)

		return nil
	}); err != nil {
		return err
	}

	resourceConfig := rest.CopyConfig(config)
	resourceConfig = rest.AddUserAgent(resourceConfig, permissionclaimlabel.ResourceControllerName)

	kcpClusterClient, err = kcpclientset.NewForConfig(resourceConfig)
	if err != nil {
		return err
	}
	dynamicClusterClient, err = kcpdynamic.NewForConfig(resourceConfig)
	if err != nil {
		return err
	}
	permissionClaimLabelResourceController, err := permissionclaimlabel.NewResourceController(
		kcpClusterClient,
		dynamicClusterClient,
		ddsif,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(postStartHookName(permissionclaimlabel.ResourceControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(permissionclaimlabel.ResourceControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go permissionClaimLabelResourceController.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	deletionConfig := rest.CopyConfig(config)
	deletionConfig = rest.AddUserAgent(deletionConfig, apibindingdeletion.ControllerName)

	kcpClusterClient, err = kcpclientset.NewForConfig(deletionConfig)
	if err != nil {
		return err
	}
	metadataClient, err := kcpmetadata.NewForConfig(deletionConfig)
	if err != nil {
		return err
	}
	apibindingDeletionController := apibindingdeletion.NewController(
		metadataClient,
		kcpClusterClient,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)

	return server.AddPostStartHook(postStartHookName(apibindingdeletion.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apibindingdeletion.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go apibindingDeletionController.Start(goContext(hookContext), 10)

		return nil
	})
}

func (s *Server) installAPIBinderController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	// Client used to create APIBindings within the initializing workspace
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, initialization.ControllerName)
	// TODO(ncdc): support standalone vw server when --shard-virtual-workspace-url is set
	config.Host += initializingworkspacesbuilder.URLFor(tenancyv1alpha1.ClusterWorkspaceAPIBindingsInitializer)
	initializingWorkspacesKcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	informerClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	// This informer factory is created here because it is specifically against the initializing workspaces virtual
	// workspace.
	initializingWorkspacesKcpInformers := kcpinformers.NewSharedInformerFactoryWithOptions(
		informerClient,
		resyncPeriod,
	)

	c, err := initialization.NewAPIBinder(
		initializingWorkspacesKcpClusterClient,
		initializingWorkspacesKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceTypes(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(initialization.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(initialization.ControllerName))

		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		initializingWorkspacesKcpInformers.Start(hookContext.StopCh)
		initializingWorkspacesKcpInformers.WaitForCacheSync(hookContext.StopCh)

		go c.Start(goContext(hookContext), 2)
		return nil
	})
}

func (s *Server) installCRDCleanupController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, crdcleanup.ControllerName)

	crdClusterClient, err := kcpapiextensionsclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := crdcleanup.NewController(
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		crdClusterClient,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(crdcleanup.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(crdcleanup.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installAPIExportController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiexport.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiexport.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
		kubeClusterClient,
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.KubeSharedInformerFactory.Core().V1().Secrets(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(apiexport.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apiexport.ControllerName))
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return crdsSynced && exportsSynced && bindingsSynced, nil
		}); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installSchedulingLocationStatusController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	controllerName := "kcp-scheduling-location-status-controller"
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, controllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulinglocationstatus.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(controllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(controllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installWorkloadNamespaceScheduler(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadnamespace.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadnamespace.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(postStartHookName(workloadnamespace.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadnamespace.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) installWorkloadPlacementScheduler(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadplacement.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadplacement.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(workloadplacement.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadplacement.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installSchedulingPlacementController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, schedulingplacement.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := schedulingplacement.NewController(
		kcpClusterClient,
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Placements(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(schedulingplacement.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(schedulingplacement.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installWorkloadsAPIExportController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadsapiexport.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexport.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.KcpSharedInformerFactory.Apiresource().V1alpha1().NegotiatedAPIResources(),
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(workloadsapiexport.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexport.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installExtraAnnotationSyncController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, extraannotationsync.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := extraannotationsync.NewController(kcpClusterClient,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(extraannotationsync.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexport.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installWorkloadsAPIExportCreateController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, workloadsapiexportcreate.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := workloadsapiexportcreate.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Scheduling().V1alpha1().Locations(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(workloadsapiexportcreate.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexportcreate.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installWorkloadsSyncTargetExportController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, synctargetexports.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := synctargetexports.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.KcpSharedInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(synctargetexports.ControllerName, func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexportcreate.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installSyncTargetController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, synctargetcontroller.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := synctargetcontroller.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaceShards(),
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(synctargetcontroller.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(synctargetcontroller.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installKubeQuotaController(
	ctx context.Context,
	config *rest.Config,
	server *genericapiserver.GenericAPIServer,
) error {
	config = rest.CopyConfig(config)
	// TODO(ncdc): figure out if we need config
	config = rest.AddUserAgent(config, kubequota.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	// TODO(ncdc): should we make these configurable?
	const (
		quotaResyncPeriod        = 5 * time.Minute
		replenishmentPeriod      = 12 * time.Hour
		workersPerLogicalCluster = 1
	)

	c, err := kubequota.NewController(
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		kubeClusterClient,
		s.KubeSharedInformerFactory,
		s.DynamicDiscoverySharedInformerFactory,
		quotaResyncPeriod,
		replenishmentPeriod,
		workersPerLogicalCluster,
		s.syncedCh,
	)
	if err != nil {
		return err
	}

	if err := server.AddPostStartHook(postStartHookName(kubequota.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(kubequota.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	if err := server.AddPreShutdownHook(kubequota.ControllerName, func() error {
		close(s.quotaAdmissionStopCh)
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) installApiExportIdentityController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	if s.Options.Extra.ShardName == tenancyv1alpha1.RootShard {
		return nil
	}
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, identitycache.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	c, err := identitycache.NewApiExportIdentityProviderController(kubeClusterClient, s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIExports(), s.KubeSharedInformerFactory.Core().V1().ConfigMaps())
	if err != nil {
		return err
	}
	return server.AddPostStartHook(postStartHookName(identitycache.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(identitycache.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 1)
		return nil
	})
}

func (s *Server) installReplicationController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	if !s.Options.Cache.Enabled {
		return nil
	}

	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, replication.ControllerName)
	dynamicLocalClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	controller, err := replication.NewController(s.Options.Extra.ShardName, s.CacheDynamicClient, dynamicLocalClient, s.KcpSharedInformerFactory, s.CacheKcpSharedInformerFactory)
	if err != nil {
		return err
	}
	return server.AddPostStartHook(postStartHookName(replication.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(replication.ControllerName))
		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		if err := s.waitForOptionalSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go controller.Start(goContext(hookContext), 2)
		return nil
	})
}

func (s *Server) installGarbageCollectorController(ctx context.Context, config *rest.Config, server *genericapiserver.GenericAPIServer) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, garbagecollector.ControllerName)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	metadataClient, err := kcpmetadata.NewForConfig(config)
	if err != nil {
		return err
	}

	// TODO: make it configurable
	const (
		workersPerLogicalCluster = 1
	)

	c, err := garbagecollector.NewController(
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces(),
		kubeClusterClient,
		metadataClient,
		s.DynamicDiscoverySharedInformerFactory,
		workersPerLogicalCluster,
		s.syncedCh,
	)
	if err != nil {
		return err
	}

	return server.AddPostStartHook(postStartHookName(garbagecollector.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(garbagecollector.ControllerName))

		if err := s.waitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
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

// waitForOptionalSync waits until optional informers have been synced.
// use this method before starting controllers that require additional informers.
func (s *Server) waitForOptionalSync(stop <-chan struct{}) error {
	select {
	case <-stop:
		return errors.New("timed out waiting for optional informers to sync")
	case <-s.syncedCh:
		return nil
	}
}
