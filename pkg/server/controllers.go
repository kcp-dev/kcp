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

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apiextensionsscheme "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginvalidatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/cel/openapi/resolver"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/certificates/rootcacertpublisher"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/controller/validatingadmissionpolicystatus"
	"k8s.io/kubernetes/pkg/generated/openapi"
	"k8s.io/kubernetes/pkg/serviceaccount"

	configuniversal "github.com/kcp-dev/kcp/config/universal"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/informer"
	permissionclaimlabler "github.com/kcp-dev/kcp/pkg/permissionclaim"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibindingdeletion"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexport"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexportendpointslice"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiexportendpointsliceurls"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/crdcleanup"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/extraannotationsync"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/identitycache"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/logicalclustercleanup"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/permissionclaimlabel"
	apisreplicateclusterrole "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicateclusterrole"
	apisreplicateclusterrolebinding "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicateclusterrolebinding"
	apisreplicatelogicalcluster "github.com/kcp-dev/kcp/pkg/reconciler/apis/replicatelogicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterrolebindings"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterroles"
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
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacemounts"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacetype"
	"github.com/kcp-dev/kcp/pkg/reconciler/topology/partitionset"
	initializingworkspacesbuilder "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/builder"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

type RunFunc func(ctx context.Context)
type WaitFunc func(ctx context.Context, s *Server) error

const (
	waitPollInterval = time.Millisecond * 100
)

type controllerWrapper struct {
	Name   string
	Runner RunFunc
	Wait   WaitFunc
}

func (s *Server) startControllers(ctx context.Context) {
	for _, controller := range s.controllers {
		go s.runController(ctx, controller)
	}
}

func (s *Server) runController(ctx context.Context, controller *controllerWrapper) {
	log := klog.FromContext(ctx).WithValues("controller", controller.Name)
	log.Info("waiting for sync")

	// controllers can define their own custom wait functions in case
	// they need to start early. If they do not define one, we will wait
	// for everything to sync.
	var err error
	if controller.Wait != nil {
		err = controller.Wait(ctx, s)
	} else {
		err = s.WaitForSync(ctx.Done())
	}
	if err != nil {
		log.Error(err, "failed to wait for sync")
		return
	}

	log.Info("starting registered controller")
	controller.Runner(ctx)
}

func (s *Server) registerController(controller *controllerWrapper) error {
	if s.controllers[controller.Name] != nil {
		return fmt.Errorf("controller %s is already registered", controller.Name)
	}

	s.controllers[controller.Name] = controller

	return nil
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

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Run(ctx, 5)
		},
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
		ctx,
		kubeClient,
		metadata,
		discoverResourcesFn,
		s.KubeSharedInformerFactory.Core().V1().Namespaces(),
		time.Duration(5)*time.Minute,
		corev1.FinalizerKubernetes,
	)

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Core().V1().Namespaces().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Run(ctx, 10)
		},
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

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Core().V1().ServiceAccounts().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().Namespaces().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Run(ctx, 1)
		},
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
		},
	)
	if err != nil {
		return fmt.Errorf("error creating service account controller: %w", err)
	}

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Core().V1().ServiceAccounts().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().Secrets().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			controller.Run(
				ctx,
				int(s.Options.Controllers.SAController.ConcurrentSATokenSyncs),
			)
		},
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

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Core().V1().ConfigMaps().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().Namespaces().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Run(ctx, 2)
		},
	})
}

func (s *Server) installKubeValidatingAdmissionPolicyStatusController(_ context.Context, config *rest.Config) error {
	controllerName := fmt.Sprintf("kube-%s", validatingadmissionpolicystatus.ControllerName)
	config = rest.AddUserAgent(rest.CopyConfig(config), controllerName)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	schemaResolver := resolver.NewDefinitionsSchemaResolver(openapi.GetOpenAPIDefinitions, k8sscheme.Scheme, apiextensionsscheme.Scheme)

	typeCheckerFn := func(clusterName logicalcluster.Path) (*pluginvalidatingadmissionpolicy.TypeChecker, error) {
		logicalClusterConfig := rest.CopyConfig(config)
		logicalClusterConfig.Host += clusterName.RequestPath()
		kubeClient, err := kcpkubernetesclientset.NewForConfig(config)
		if err != nil {
			return nil, err
		}

		discoveryClient := memory.NewMemCacheClient(kubeClient.Cluster(clusterName).Discovery())

		return &pluginvalidatingadmissionpolicy.TypeChecker{
			SchemaResolver: schemaResolver.Combine(&resolver.ClientDiscoveryResolver{Discovery: discoveryClient}),
			RestMapper:     restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient),
		}, nil
	}

	c, err := validatingadmissionpolicystatus.NewController(
		s.KubeSharedInformerFactory.Admissionregistration().V1().ValidatingAdmissionPolicies(),
		kubeClient.AdmissionregistrationV1().ValidatingAdmissionPolicies(),
		typeCheckerFn)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Admissionregistration().V1().ValidatingAdmissionPolicies().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Run(ctx, 5)
		},
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

	return s.registerController(&controllerWrapper{
		Name: tenancylogicalcluster.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			controller.Start(ctx, 10)
		},
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
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)

	return s.registerController(&controllerWrapper{
		Name: logicalclusterdeletion.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			logicalClusterDeletionController.Start(ctx, 10)
		},
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

	if err := s.registerController(&controllerWrapper{
		Name: workspace.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			workspaceController.Start(ctx, 2)
		},
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
		if err := s.registerController(&controllerWrapper{
			Name: shard.ControllerName,
			Wait: func(ctx context.Context, s *Server) error {
				return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
					return s.KcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced(), nil
				})
			},
			Runner: func(ctx context.Context) {
				workspaceShardController.Start(ctx, 2)
			},
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

	if err := s.registerController(&controllerWrapper{
		Name: workspacetype.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			workspaceTypeController.Start(ctx, 2)
		},
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
		sets.New[string](s.Options.Extra.BatteriesIncluded...),
	)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: universalControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			universalController.Start(ctx, 2)
		},
	})
}

func (s *Server) installWorkspaceMountsScheduler(ctx context.Context, config *rest.Config) error {
	// TODO(mjudeikis): Remove this and move to batteries.
	if !kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.WorkspaceMounts) {
		return nil
	}

	// NOTE: keep `config` unaltered so there isn't cross-use between controllers installed here.
	workspaceConfig := rest.CopyConfig(config)
	workspaceConfig = rest.AddUserAgent(workspaceConfig, workspacemounts.ControllerName)
	kcpClusterClient, err := kcpclientset.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}

	dynamicClusterClient, err := kcpdynamic.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}

	workspaceMountsController, err := workspacemounts.NewController(
		kcpClusterClient,
		dynamicClusterClient,
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
		s.DiscoveringDynamicSharedInformerFactory,
	)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: workspacemounts.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				_, notSynced := s.DiscoveringDynamicSharedInformerFactory.Informers()
				if len(notSynced) > 0 {
					return false, nil
				}
				return s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			workspaceMountsController.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: logicalclusterctrl.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			logicalClusterController.Start(ctx, 2)
		},
	})
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
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIConversions(),
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
	)
	if err != nil {
		return err
	}

	if err := s.registerController(&controllerWrapper{
		Name: apibinding.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
			// and the controllers must run as soon as these two informers are up in order to bootstrap
			// the rest of the system. Everything else in the kcp clientset is APIBinding based.
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	if err := s.registerController(&controllerWrapper{
		Name: permissionclaimlabel.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			permissionClaimLabelController.Start(ctx, 5)
		},
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

	if err := s.registerController(&controllerWrapper{
		Name: permissionclaimlabel.ResourceControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			permissionClaimLabelResourceController.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: apibindingdeletion.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			apibindingDeletionController.Start(ctx, 10)
		},
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

	return s.registerController(&controllerWrapper{
		Name: initialization.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			initializingWorkspacesKcpInformers.Start(ctx.Done())
			initializingWorkspacesKcpInformers.WaitForCacheSync(ctx.Done())

			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: crdcleanup.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
	})
}

func (s *Server) installLogicalClusterCleanupController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, logicalclustercleanup.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := logicalclustercleanup.NewController(
		kcpClusterClient,
		s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: logicalclustercleanup.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced() &&
					s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: apiexport.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
			// and the controllers must run as soon as these two informers are up in order to bootstrap
			// the rest of the system. Everything else in the kcp clientset is APIBinding based.
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().Namespaces().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().Secrets().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: apisreplicateclusterrole.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: coresreplicateclusterrole.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: apisreplicateclusterrolebinding.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: apisreplicatelogicalcluster.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicatelogicalcluster.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: corereplicateclusterrolebinding.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicateclusterrole.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicateclusterrolebinding.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
	})
}

func (s *Server) installAPIExportEndpointSliceController(_ context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiexportendpointslice.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiexportendpointslice.NewController(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		// Shards and APIExports get retrieved from cache server
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions(),
		kcpClusterClient,
	)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: apiexportendpointslice.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
	})
}

func (s *Server) installAPIExportEndpointSliceURLsController(_ context.Context, _ *rest.Config) error {
	config := rest.CopyConfig(s.ExternalLogicalClusterAdminConfig)
	config = rest.AddUserAgent(config, apiexportendpointsliceurls.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	c, err := apiexportendpointsliceurls.NewController(
		s.Options.Extra.ShardName,
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		// Shards and APIExports get retrieved from cache server
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		kcpClusterClient,
	)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: apiexportendpointsliceurls.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: partitionset.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Topology().V1alpha1().PartitionSets().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions().Informer().HasSynced() &&
					s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: extraannotationsync.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
					s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: kubequota.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				_, notSynced := s.DiscoveringDynamicSharedInformerFactory.Informers()
				if len(notSynced) > 0 {
					return false, nil
				}
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced() &&
					s.KubeSharedInformerFactory.Core().V1().ResourceQuotas().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: identitycache.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				return s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced() && s.KubeSharedInformerFactory.Core().V1().ConfigMaps().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 1)
		},
	})
}

func (s *Server) installReplicationController(ctx context.Context, config *rest.Config, gvrs map[schema.GroupVersionResource]replication.ReplicatedGVR) error {
	// TODO(sttts): set user agent
	controller, err := replication.NewController(s.Options.Extra.ShardName, s.CacheDynamicClient, gvrs)
	if err != nil {
		return err
	}

	return s.registerController(&controllerWrapper{
		Name: replication.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				for _, gvr := range controller.Gvrs {
					if !gvr.Local.HasSynced() || !gvr.Global.HasSynced() {
						return false, nil
					}
				}
				return true, nil
			})
		},
		Runner: func(ctx context.Context) {
			controller.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: garbagecollector.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, waitPollInterval, true, func(ctx context.Context) (bool, error) {
				_, notSynced := s.DiscoveringDynamicSharedInformerFactory.Informers()
				if len(notSynced) > 0 {
					return false, nil
				}
				return s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced(), nil
			})
		},
		Runner: func(ctx context.Context) {
			c.Start(ctx, 2)
		},
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

// addIndexerstoInformers is separated out from controllers as the re-election calls for controller re-initialization,
// it would panics in indexer addition to informers as they are already started at bootup.
func (s *Server) addIndexersToInformers(_ context.Context) map[schema.GroupVersionResource]replication.ReplicatedGVR {
	permissionclaimlabel.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	permissionclaimlabler.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports())
	apibinding.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
	)
	apiexport.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports())
	apiexportendpointslice.InstallIndexers(
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
	)
	apiexportendpointsliceurls.InstallIndexers(
		s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	labelclusterrolebindings.InstallIndexers(
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
	)
	labelclusterroles.InstallIndexers(
		s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
	)
	workspace.InstallIndexers(
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
		s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
		s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
	)
	workspacemounts.InstallIndexers(
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
	)
	extraannotationsync.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	initialization.InstallIndexers(
		s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
		s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes())
	crdcleanup.InstallIndexers(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
	)
	return replication.InstallIndexers(
		s.KcpSharedInformerFactory,
		s.CacheKcpSharedInformerFactory,
		s.KubeSharedInformerFactory,
		s.CacheKubeSharedInformerFactory,
	)
}
