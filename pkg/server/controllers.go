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
	"github.com/kcp-dev/logicalcluster/v3"

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
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibindingdeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexportendpointslice"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/crdcleanup"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/extraannotationsync"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/permissionclaimlabel"
	apisreplicateclusterrole "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicateclusterrole"
	apisreplicateclusterrolebinding "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicateclusterrolebinding"
	apisreplicatelogicalcluster "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicatelogicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	logicalclusterctrl "github.com/kcp-dev/kcp/pkg/reconciler/core/logicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/core/logicalclusterdeletion"
	coresreplicateclusterrole "github.com/kcp-dev/kcp/pkg/reconciler/core/replicateclusterrole"
	corereplicateclusterrolebinding "github.com/kcp-dev/kcp/pkg/reconciler/core/replicateclusterrolebinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/core/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/garbagecollector"
	"github.com/kcp-dev/kcp/pkg/reconciler/kubequota"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/bootstrap"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization"
	tenancylogicalcluster "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/logicalcluster"
	tenancyreplicateclusterrole "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/replicateclusterrole"
	tenancyreplicateclusterrolebinding "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/replicateclusterrolebinding"
	tenancyreplicatelogicalcluster "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/replicatelogicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspace"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacetype"
	"github.com/kcp-dev/kcp/pkg/reconciler/topology/partitionset"
	workloadsapiexport "github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
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

	discoverResourcesFn := func(clusterName logicalcluster.Path) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += clusterName.RequestPath()
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
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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

func (s *Server) installTenancyLogicalClusterController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, tenancylogicalcluster.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	controller := tenancylogicalcluster.NewController(
		kubeClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
	)

	return s.AddPostStartHook(postStartHookName(tenancylogicalcluster.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(tenancylogicalcluster.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go controller.Start(ctx, 10)
		return nil
	})
}

func (s *Server) installLogicalClusterDeletionController(ctx context.Context, config *rest.Config, logicalClusterAdminConfig, externalLogicalClusterAdminConfig *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, logicalclusterdeletion.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	metadataClusterClient, err := kcpmetadata.NewForConfig(config)
	if err != nil {
		return err
	}
	discoverResourcesFn := func(clusterName logicalcluster.Path) ([]*metav1.APIResourceList, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += clusterName.RequestPath()
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

	logicalClusterDeletionController := logicalclusterdeletion.NewController(
		kubeClusterClient,
		kcpClusterClient,
		logicalClusterAdminConfig,
		externalLogicalClusterAdminConfig,
		metadataClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		discoverResourcesFn,
	)

	return s.AddPostStartHook(postStartHookName(logicalclusterdeletion.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(logicalclusterdeletion.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go logicalClusterDeletionController.Start(ctx, 10)
		return nil
	})
}

func (s *Server) installWorkspaceScheduler(ctx context.Context, config *rest.Config, logicalClusterAdminConfig, externalLogicalClusterAdminConfig *rest.Config) error {
	// NOTE: keep `config` unaltered so there isn't cross-use between controllers installed here.
	workspaceConfig := rest.CopyConfig(config)
	workspaceConfig = rest.AddUserAgent(workspaceConfig, workspace.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}

	logicalClusterAdminConfig = rest.CopyConfig(logicalClusterAdminConfig)
	logicalClusterAdminConfig = rest.AddUserAgent(logicalClusterAdminConfig, workspace.ControllerName+"+"+s.Options.Extra.ShardName)

	externalLogicalClusterAdminConfig = rest.CopyConfig(externalLogicalClusterAdminConfig)
	externalLogicalClusterAdminConfig = rest.AddUserAgent(externalLogicalClusterAdminConfig, workspace.ControllerName+"+"+s.Options.Extra.ShardName)

	workspaceController, err := workspace.NewController(
		s.Options.Extra.ShardName,
		kcpClusterClient,
		kubeClusterClient,
		logicalClusterAdminConfig,
		externalLogicalClusterAdminConfig,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(workspace.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workspace.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go workspaceController.Start(ctx, 2)
		return nil
	}); err != nil {
		return err
	}

	clusterShardConfig := rest.CopyConfig(config)
	clusterShardConfig = rest.AddUserAgent(clusterShardConfig, shard.ControllerName)
	kcpClusterClient, err = kcpclientset.NewForConfig(clusterShardConfig)
	if err != nil {
		return err
	}

	var workspaceShardController *shard.Controller
	if s.Options.Extra.ShardName == corev1alpha1.RootShard {
		workspaceShardController, err = shard.NewController(
			kcpClusterClient,
			s.KcpSharedInformerFactory.Core().V1alpha1().Shards(),
		)
		if err != nil {
			return err
		}
	}
	if workspaceShardController != nil {
		if err := s.AddPostStartHook(postStartHookName(shard.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
			logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(shard.ControllerName))
			if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
	workspaceTypeConfig = rest.AddUserAgent(workspaceTypeConfig, workspacetype.ControllerName)
	kcpClusterClient, err = kcpclientset.NewForConfig(workspaceTypeConfig)
	if err != nil {
		return err
	}

	workspaceTypeController, err := workspacetype.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(workspacetype.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workspacetype.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
	bootstrapConfig.Impersonate.UserName = KcpBootstrapperUserName
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
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		tenancyv1alpha1.WorkspaceTypeReference{Path: "root", Name: "universal"},
		configuniversal.Bootstrap,
		sets.NewString(s.Options.Extra.BatteriesIncluded...),
	)
	if err != nil {
		return err
	}
	return s.AddPostStartHook(postStartHookName(universalControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(universalControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go universalController.Start(ctx, 2)
		return nil
	})
}

func (s *Server) installLogicalCluster(ctx context.Context, config *rest.Config) error {
	logicalClusterConfig := rest.CopyConfig(config)
	logicalClusterConfig = rest.AddUserAgent(logicalClusterConfig, logicalclusterctrl.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(logicalClusterConfig)
	if err != nil {
		return err
	}

	logicalClusterController, err := logicalclusterctrl.NewController(
		s.CompletedConfig.ShardExternalURL,
		kcpClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(logicalclusterctrl.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(logicalclusterctrl.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		go logicalClusterController.Start(ctx, 2)
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (s *Server) installAPIBindingController(ctx context.Context, config *rest.Config, ddsif *informer.DiscoveringDynamicSharedInformerFactory) error {
	// NOTE: keep `config` unaltered so there isn't cross-use between controllers installed here.
	apiBindingConfig := rest.CopyConfig(config)
	apiBindingConfig = rest.AddUserAgent(apiBindingConfig, apibinding.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(apiBindingConfig)
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
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIConversions(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIConversions(),
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(apibinding.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apibinding.ControllerName))
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			cacheExportsSynced := s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			schemasSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced()
			cacheSchemasSynced := s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced()
			bindingsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return crdsSynced && exportsSynced && cacheExportsSynced && schemasSynced && cacheSchemasSynced && bindingsSynced, nil
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
	dynamicClusterClient, err := kcpdynamic.NewForConfig(permissionClaimLabelConfig)
	if err != nil {
		return err
	}

	permissionClaimLabelController, err := permissionclaimlabel.NewController(
		kcpClusterClient,
		dynamicClusterClient,
		ddsif,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(permissionclaimlabel.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(permissionclaimlabel.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(permissionclaimlabel.ResourceControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(permissionclaimlabel.ResourceControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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

	return s.AddPostStartHook(postStartHookName(apibindingdeletion.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apibindingdeletion.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go apibindingDeletionController.Start(goContext(hookContext), 10)

		return nil
	})
}

func (s *Server) installAPIBinderController(ctx context.Context, config *rest.Config) error {
	// Client used to create APIBindings within the initializing workspace
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, initialization.ControllerName)
	config.Host += initializingworkspacesbuilder.URLFor(tenancyv1alpha1.WorkspaceAPIBindingsInitializer)

	if !s.Options.Virtual.Enabled && s.Options.Extra.ShardVirtualWorkspaceURL != "" {
		vwURL := fmt.Sprintf("https://%s", s.GenericConfig.ExternalAddress)
		if s.Options.Extra.ShardVirtualWorkspaceCAFile == "" {
			// TODO move verification up
			return fmt.Errorf("s.Options.Extra.ShardVirtualWorkspaceCAFile is required")
		}
		if s.Options.Extra.ShardClientCertFile == "" {
			// TODO move verification up
			return fmt.Errorf("s.Options.Extra.ShardClientCertFile is required")
		}
		if s.Options.Extra.ShardClientKeyFile == "" {
			// TODO move verification up
			return fmt.Errorf("s.Options.Extra.ShardClientKeyFile is required")
		}
		config.TLSClientConfig.CAFile = s.Options.Extra.ShardVirtualWorkspaceCAFile
		config.TLSClientConfig.CertFile = s.Options.Extra.ShardClientCertFile
		config.TLSClientConfig.KeyFile = s.Options.Extra.ShardClientKeyFile
		config.Host = fmt.Sprintf("%v%v", vwURL, initializingworkspacesbuilder.URLFor(tenancyv1alpha1.WorkspaceAPIBindingsInitializer))
	}

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
		initializingWorkspacesKcpInformers.Core().V1alpha1().LogicalClusters(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
		s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(initialization.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(initialization.ControllerName))

		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		initializingWorkspacesKcpInformers.Start(hookContext.StopCh)
		initializingWorkspacesKcpInformers.WaitForCacheSync(hookContext.StopCh)

		go c.Start(goContext(hookContext), 2)
		return nil
	})
}

func (s *Server) installCRDCleanupController(ctx context.Context, config *rest.Config) error {
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

	return s.AddPostStartHook(postStartHookName(crdcleanup.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(crdcleanup.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installAPIExportController(ctx context.Context, config *rest.Config) error {
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
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		kubeClusterClient,
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		s.KubeSharedInformerFactory.Core().V1().Secrets(),
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(apiexport.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apiexport.ControllerName))
		// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
		// and the controllers must run as soon as these two informers are up in order to bootstrap
		// the rest of the system. Everything else in the kcp clientset is APIBinding based.
		if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			return crdsSynced && exportsSynced, nil
		}); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installApisReplicateClusterRoleControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apisreplicateclusterrole.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := apisreplicateclusterrole.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
	)

	return s.AddPostStartHook(postStartHookName(apisreplicateclusterrole.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apisreplicateclusterrole.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installCoreReplicateClusterRoleControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, coresreplicateclusterrole.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := coresreplicateclusterrole.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
	)

	return s.AddPostStartHook(postStartHookName(coresreplicateclusterrole.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(coresreplicateclusterrole.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installApisReplicateClusterRoleBindingControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apisreplicateclusterrolebinding.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := apisreplicateclusterrolebinding.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
	)

	return s.AddPostStartHook(postStartHookName(apisreplicateclusterrolebinding.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apisreplicateclusterrolebinding.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installApisReplicateLogicalClusterControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apisreplicatelogicalcluster.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := apisreplicatelogicalcluster.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)

	return s.AddPostStartHook(postStartHookName(apisreplicatelogicalcluster.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apisreplicatelogicalcluster.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installTenancyReplicateLogicalClusterControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, tenancyreplicatelogicalcluster.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := tenancyreplicatelogicalcluster.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
	)

	return s.AddPostStartHook(postStartHookName(tenancyreplicatelogicalcluster.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(tenancyreplicatelogicalcluster.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installCoreReplicateClusterRoleBindingControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, corereplicateclusterrolebinding.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := corereplicateclusterrolebinding.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
	)

	return s.AddPostStartHook(postStartHookName(corereplicateclusterrolebinding.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(corereplicateclusterrolebinding.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installTenancyReplicateClusterRoleControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, tenancyreplicateclusterrole.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := tenancyreplicateclusterrole.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
	)

	return s.AddPostStartHook(postStartHookName(tenancyreplicateclusterrole.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(tenancyreplicateclusterrole.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installTenancyReplicateClusterRoleBindingControllers(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, tenancyreplicateclusterrolebinding.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c := tenancyreplicateclusterrolebinding.NewController(
		kubeClusterClient,
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
	)

	return s.AddPostStartHook(postStartHookName(tenancyreplicateclusterrolebinding.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(tenancyreplicateclusterrolebinding.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installAPIExportEndpointSliceController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiexportendpointslice.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiexportendpointslice.NewController(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		// Shards and APIExports get retrieved from cache server
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions(),
		kcpClusterClient,
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(apiexportendpointslice.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(apiexportendpointslice.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installPartitionSetController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, partitionset.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := partitionset.NewController(
		s.KcpSharedInformerFactory.Topology().V1alpha1().PartitionSets(),
		s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions(),
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		kcpClusterClient,
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(partitionset.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(partitionset.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) installExtraAnnotationSyncController(ctx context.Context, config *rest.Config) error {
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

	return s.AddPostStartHook(postStartHookName(extraannotationsync.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(workloadsapiexport.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
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
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		kubeClusterClient,
		s.KubeSharedInformerFactory,
		s.DiscoveringDynamicSharedInformerFactory,
		quotaResyncPeriod,
		replenishmentPeriod,
		workersPerLogicalCluster,
		s.syncedCh,
	)
	if err != nil {
		return err
	}

	if err := s.AddPostStartHook(postStartHookName(kubequota.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(kubequota.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	}); err != nil {
		return err
	}

	return s.AddPreShutdownHook(kubequota.ControllerName, func() error {
		close(s.quotaAdmissionStopCh)
		return nil
	})
}

func (s *Server) installApiExportIdentityController(ctx context.Context, config *rest.Config) error {
	if s.Options.Extra.ShardName == corev1alpha1.RootShard {
		return nil
	}
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, identitycache.ControllerName)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	c, err := identitycache.NewApiExportIdentityProviderController(kubeClusterClient, s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(), s.KubeSharedInformerFactory.Core().V1().ConfigMaps())
	if err != nil {
		return err
	}
	return s.AddPostStartHook(postStartHookName(identitycache.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(identitycache.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 1)
		return nil
	})
}

func (s *Server) installReplicationController(ctx context.Context, config *rest.Config) error {
	// TODO(sttts): set user agent
	controller, err := replication.NewController(s.Options.Extra.ShardName, s.CacheDynamicClient, s.KcpSharedInformerFactory, s.CacheKcpSharedInformerFactory, s.KubeSharedInformerFactory, s.CacheKubeSharedInformerFactory)
	if err != nil {
		return err
	}
	return s.AddPostStartHook(postStartHookName(replication.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(replication.ControllerName))
		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go controller.Start(goContext(hookContext), 2)
		return nil
	})
}

func (s *Server) installGarbageCollectorController(ctx context.Context, config *rest.Config) error {
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
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		kubeClusterClient,
		metadataClient,
		s.DiscoveringDynamicSharedInformerFactory,
		workersPerLogicalCluster,
		s.syncedCh,
	)
	if err != nil {
		return err
	}

	return s.AddPostStartHook(postStartHookName(garbagecollector.ControllerName), func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", postStartHookName(garbagecollector.ControllerName))

		if err := s.WaitForSync(hookContext.StopCh); err != nil {
			logger.Error(err, "failed to finish post-start-hook")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		go c.Start(goContext(hookContext), 2)

		return nil
	})
}

func (s *Server) WaitForSync(stop <-chan struct{}) error {
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
