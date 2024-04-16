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
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	admissionregistrationv1alpha1 "k8s.io/api/admissionregistration/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/controller/certificates/rootcacertpublisher"
	"k8s.io/kubernetes/pkg/controller/clusterroleaggregation"
	"k8s.io/kubernetes/pkg/controller/namespace"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	"k8s.io/kubernetes/pkg/serviceaccount"

	configuniversal "github.com/kcp-dev/kcp/config/universal"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
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
	"github.com/kcp-dev/kcp/pkg/server/openapiv3"
	initializingworkspacesbuilder "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/builder"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/sdk/client"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	byBase36Sha224Name                      = "byBase36Sha224Name"
	unschedulable                           = "unschedulable"
	indexAPIExportsByAPIResourceSchema      = "apiExportsByAPIResourceSchema"
	indexAPIExportEndpointSliceByAPIExport  = "indexAPIExportEndpointSliceByAPIExport"
	indexAPIExportEndpointSlicesByPartition = "indexAPIExportEndpointSlicesByPartition"
)

type RunFunc func(ctx context.Context)
type WaitFunc func(ctx context.Context, s *Server) error

type controllerWrapper struct {
	Name   string
	Runner RunFunc
	Wait   WaitFunc
}

func indexUnschedulable(obj interface{}) ([]string, error) {
	workspace := obj.(*tenancyv1alpha1.Workspace)
	if conditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && conditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable {
		return []string{"true"}, nil
	}
	return []string{}, nil
}

func indexByBase36Sha224Name(obj interface{}) ([]string, error) {
	s := obj.(*corev1alpha1.Shard)
	return []string{workspace.ByBase36Sha224NameValue(s.Name)}, nil
}

// indexAPIExportsByAPIResourceSchemasFunc is an index function that maps an APIExport to its spec.latestResourceSchemas.
func indexAPIExportsByAPIResourceSchemasFunc(obj interface{}) ([]string, error) {
	apiExport, ok := obj.(*apisv1alpha1.APIExport)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExport, but is %T", obj)
	}

	ret := make([]string, len(apiExport.Spec.LatestResourceSchemas))
	for i := range apiExport.Spec.LatestResourceSchemas {
		ret[i] = client.ToClusterAwareKey(logicalcluster.From(apiExport).Path(), apiExport.Spec.LatestResourceSchemas[i])
	}

	return ret, nil
}

// indexAPIExportEndpointSliceByAPIExportFunc indexes the APIExportEndpointSlice by their APIExport's Reference Path and Name.
func indexAPIExportEndpointSliceByAPIExportFunc(obj interface{}) ([]string, error) {
	apiExportEndpointSlice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExportEndpointSlice", obj)
	}

	path := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if path.Empty() {
		path = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	return []string{path.Join(apiExportEndpointSlice.Spec.APIExport.Name).String()}, nil
}

// indexAPIExportEndpointSlicesByPartitionFunc is an index function that maps a Partition to the key for its
// spec.partition.
func indexAPIExportEndpointSlicesByPartitionFunc(obj interface{}) ([]string, error) {
	slice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIExportEndpointSlice, but is %T", obj)
	}

	if slice.Spec.Partition != "" {
		clusterName := logicalcluster.From(slice).Path()
		if !ok {
			// this will never happen due to validation
			return []string{}, fmt.Errorf("cluster information missing")
		}
		key := client.ToClusterAwareKey(clusterName, slice.Spec.Partition)
		return []string{key}, nil
	}

	return []string{}, nil
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

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Runner: func(ctx context.Context) {
			c := clusterroleaggregation.NewClusterRoleAggregation(
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
				kubeClient.RbacV1())
			c.Run(ctx, 5)
		},
	})
}

func (s *Server) installOpenAPIv3Controller(ctx context.Context, config *rest.Config) error {
	controllerName := openapiv3.ControllerName

	return s.registerController(&controllerWrapper{
		Name: controllerName,
		Runner: func(ctx context.Context) {
			s.openAPIv3Controller.Run(ctx)
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
		Runner: func(ctx context.Context) {
			c.Run(ctx, 2)
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

	return s.registerController(&controllerWrapper{
		Name: tenancylogicalcluster.ControllerName,
		Runner: func(ctx context.Context) {
			controller := tenancylogicalcluster.NewController(
				kubeClusterClient,
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
			)
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

	return s.registerController(&controllerWrapper{
		Name: logicalclusterdeletion.ControllerName,
		Runner: func(ctx context.Context) {
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

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces().Informer().GetIndexer(), cache.Indexers{
		unschedulable: indexUnschedulable,
	})
	indexers.AddIfNotPresentOrDie(s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().GetIndexer(), cache.Indexers{
		byBase36Sha224Name: indexByBase36Sha224Name,
	})
	indexers.AddIfNotPresentOrDie(s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	if err := s.registerController(&controllerWrapper{
		Name: workspace.ControllerName,
		Runner: func(ctx context.Context) {
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
				panic(err)
			}
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

	if err := s.registerController(&controllerWrapper{
		Name: shard.ControllerName,
		Runner: func(ctx context.Context) {
			var workspaceShardController *shard.Controller
			if s.Options.Extra.ShardName == corev1alpha1.RootShard {
				workspaceShardController, err = shard.NewController(
					kcpClusterClient,
					s.KcpSharedInformerFactory.Core().V1alpha1().Shards(),
				)
				if err != nil {
					panic(err)
				}
			}
			if workspaceShardController != nil {
				workspaceShardController.Start(ctx, 2)
			}
		},
	}); err != nil {
		return err
	}

	workspaceTypeConfig := rest.CopyConfig(config)
	workspaceTypeConfig = rest.AddUserAgent(workspaceTypeConfig, workspacetype.ControllerName)
	kcpClusterClient, err = kcpclientset.NewForConfig(workspaceTypeConfig)
	if err != nil {
		return err
	}
	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	if err := s.registerController(&controllerWrapper{
		Name: workspacetype.ControllerName,
		Runner: func(ctx context.Context) {
			workspaceTypeController, err := workspacetype.NewController(
				kcpClusterClient,
				s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
				s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
			)
			if err != nil {
				panic(err)
			}
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
	return s.registerController(&controllerWrapper{
		Name: universalControllerName,
		Runner: func(ctx context.Context) {
			universalController, err := bootstrap.NewController(
				dynamicClusterClient,
				bootstrapKcpClusterClient,
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
				tenancyv1alpha1.WorkspaceTypeReference{Path: "root", Name: "universal"},
				configuniversal.Bootstrap,
				sets.New[string](s.Options.Extra.BatteriesIncluded...),
			)
			if err != nil {
				panic(err)
			}
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

	return s.registerController(&controllerWrapper{
		Name: workspacemounts.ControllerName,
		Runner: func(ctx context.Context) {
			workspaceMountsController, err := workspacemounts.NewController(
				kcpClusterClient,
				dynamicClusterClient,
				s.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(),
				s.DiscoveringDynamicSharedInformerFactory,
			)
			if err != nil {
				panic(err)
			}
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

	return s.registerController(&controllerWrapper{
		Name: logicalclusterctrl.ControllerName,
		Runner: func(ctx context.Context) {
			logicalClusterController, err := logicalclusterctrl.NewController(
				s.CompletedConfig.ShardExternalURL,
				kcpClusterClient,
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
			)
			if err != nil {
				panic(err)
			}
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

	// APIBinding indexers
	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})

	// APIExport indexers
	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemasFunc,
	})
	indexers.AddIfNotPresentOrDie(s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
		indexAPIExportsByAPIResourceSchema:   indexAPIExportsByAPIResourceSchemasFunc,
	})

	if err := s.registerController(&controllerWrapper{
		Name: apibinding.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			// do custom wait logic here because APIExports+APIBindings are special as system CRDs,
			// and the controllers must run as soon as these two informers are up in order to bootstrap
			// the rest of the system. Everything else in the kcp clientset is APIBinding based.
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
				exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
				cacheExportsSynced := s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
				schemasSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced()
				cacheSchemasSynced := s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Informer().HasSynced()
				bindingsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
				return crdsSynced && exportsSynced && cacheExportsSynced && schemasSynced && cacheSchemasSynced && bindingsSynced, nil
			})
		},
		Runner: func(ctx context.Context) {
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
				panic(err)
			}
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

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	if err := s.registerController(&controllerWrapper{
		Name: permissionclaimlabel.ControllerName,
		Runner: func(ctx context.Context) {
			permissionClaimLabelController, err := permissionclaimlabel.NewController(
				kcpClusterClient,
				dynamicClusterClient,
				ddsif,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
				s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
			)
			if err != nil {
				panic(err)
			}
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

	if err := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(
		cache.Indexers{
			indexers.APIBindingByClusterAndAcceptedClaimedGroupResources: indexers.IndexAPIBindingByClusterAndAcceptedClaimedGroupResources,
		},
	); err != nil {
		return err
	}

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	if err := s.registerController(&controllerWrapper{
		Name: permissionclaimlabel.ResourceControllerName,
		Runner: func(ctx context.Context) {
			permissionClaimLabelResourceController, err := permissionclaimlabel.NewResourceController(
				kcpClusterClient,
				dynamicClusterClient,
				ddsif,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
				s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
			)
			if err != nil {
				panic(err)
			}
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

	return s.registerController(&controllerWrapper{
		Name: apibindingdeletion.ControllerName,
		Runner: func(ctx context.Context) {
			apibindingDeletionController := apibindingdeletion.NewController(
				metadataClient,
				kcpClusterClient,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
			)
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

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(s.CacheKcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	return s.registerController(&controllerWrapper{
		Name: initialization.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				initializingWorkspacesKcpInformers.Start(ctx.Done())
				logicalClusterCacheSynced := initializingWorkspacesKcpInformers.Core().V1alpha1().LogicalClusters().Informer().HasSynced()
				return logicalClusterCacheSynced, nil
			})
		},
		Runner: func(ctx context.Context) {
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
				panic(err)
			}
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

	indexers.AddIfNotPresentOrDie(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIBindingByBoundResourceUID: indexers.IndexAPIBindingByBoundResourceUID,
		},
	)

	return s.registerController(&controllerWrapper{
		Name: crdcleanup.ControllerName,
		Runner: func(ctx context.Context) {
			c, err := crdcleanup.NewController(
				s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
				crdClusterClient,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
			)
			if err != nil {
				panic(err)
			}
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

	indexers.AddIfNotPresentOrDie(
		s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		cache.Indexers{
			indexers.APIExportByIdentity: indexers.IndexAPIExportByIdentity,
			indexers.APIExportBySecret:   indexers.IndexAPIExportBySecret,
		},
	)

	return s.registerController(&controllerWrapper{
		Name: apiexport.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				crdsSynced := s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced()
				exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
				return crdsSynced && exportsSynced, nil
			})
		},
		Runner: func(ctx context.Context) {
			c, err := apiexport.NewController(
				kcpClusterClient,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
				s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
				kubeClusterClient,
				s.KubeSharedInformerFactory.Core().V1().Namespaces(),
				s.KubeSharedInformerFactory.Core().V1().Secrets(),
			)
			if err != nil {
				panic(err)
			}
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
	indexers.AddIfNotPresentOrDie(s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().GetIndexer(), cache.Indexers{
		labelclusterroles.ClusterRoleBindingByClusterRoleName: labelclusterroles.IndexClusterRoleBindingByClusterRoleName,
	})
	return s.registerController(&controllerWrapper{
		Name: apisreplicateclusterrole.ControllerName,
		Runner: func(ctx context.Context) {
			c := apisreplicateclusterrole.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
			)
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
	indexers.AddIfNotPresentOrDie(s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().GetIndexer(), cache.Indexers{
		labelclusterroles.ClusterRoleBindingByClusterRoleName: labelclusterroles.IndexClusterRoleBindingByClusterRoleName,
	})
	return s.registerController(&controllerWrapper{
		Name: coresreplicateclusterrole.ControllerName,
		Runner: func(ctx context.Context) {
			c := coresreplicateclusterrole.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
			)

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
	indexers.AddIfNotPresentOrDie(s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings().Informer().GetIndexer(), cache.Indexers{
		labelclusterroles.ClusterRoleBindingByClusterRoleName: labelclusterroles.IndexClusterRoleBindingByClusterRoleName,
	})

	return s.registerController(&controllerWrapper{
		Name: apisreplicateclusterrolebinding.ControllerName,
		Runner: func(ctx context.Context) {
			c := apisreplicateclusterrolebinding.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
			)

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

	return s.registerController(&controllerWrapper{
		Name: apisreplicatelogicalcluster.ControllerName,
		Runner: func(ctx context.Context) {
			c := apisreplicatelogicalcluster.NewController(
				kcpClusterClient,
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
			)
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicatelogicalcluster.ControllerName,
		Runner: func(ctx context.Context) {
			c := tenancyreplicatelogicalcluster.NewController(
				kcpClusterClient,
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
				s.KcpSharedInformerFactory.Tenancy().V1alpha1().WorkspaceTypes(),
			)
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

	return s.registerController(&controllerWrapper{
		Name: corereplicateclusterrolebinding.ControllerName,
		Runner: func(ctx context.Context) {
			c := corereplicateclusterrolebinding.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
			)
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicateclusterrole.ControllerName,
		Runner: func(ctx context.Context) {
			c := tenancyreplicateclusterrole.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
			)
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

	return s.registerController(&controllerWrapper{
		Name: tenancyreplicateclusterrolebinding.ControllerName,
		Runner: func(ctx context.Context) {
			c := tenancyreplicateclusterrolebinding.NewController(
				kubeClusterClient,
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoleBindings(),
				s.KubeSharedInformerFactory.Rbac().V1().ClusterRoles(),
			)
			c.Start(ctx, 2)
		},
	})
}

func (s *Server) installAPIExportEndpointSliceController(ctx context.Context, config *rest.Config) error {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, apiexportendpointslice.ControllerName)

	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}

	indexers.AddIfNotPresentOrDie(s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSliceByAPIExport: indexAPIExportEndpointSliceByAPIExportFunc,
	})

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().GetIndexer(), cache.Indexers{
		indexAPIExportEndpointSlicesByPartition: indexAPIExportEndpointSlicesByPartitionFunc,
	})

	return s.registerController(&controllerWrapper{
		Name: apiexportendpointslice.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				apiexportEndpointSliceCache := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().HasSynced()
				shardCacheSynced := s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced()
				apiexportendpointcacheSynced := s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
				partitionSynced := s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions().Informer().HasSynced()
				return apiexportEndpointSliceCache && shardCacheSynced && apiexportendpointcacheSynced && partitionSynced, nil
			})
		},
		Runner: func(ctx context.Context) {
			c, err := apiexportendpointslice.NewController(
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices(),
				// Shards and APIExports get retrieved from cache server
				s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
				s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
				s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions(),
				kcpClusterClient,
			)
			if err != nil {
				panic(err)
			}
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

	return s.registerController(&controllerWrapper{
		Name: partitionset.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				partitionsetSynced := s.KcpSharedInformerFactory.Topology().V1alpha1().PartitionSets().Informer().HasSynced()
				partitionSynced := s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions().Informer().HasSynced()
				shardSynced := s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards().Informer().HasSynced()
				return partitionsetSynced && partitionSynced && shardSynced, nil
			})
		},

		Runner: func(ctx context.Context) {
			c, err := partitionset.NewController(
				s.KcpSharedInformerFactory.Topology().V1alpha1().PartitionSets(),
				s.KcpSharedInformerFactory.Topology().V1alpha1().Partitions(),
				s.CacheKcpSharedInformerFactory.Core().V1alpha1().Shards(),
				kcpClusterClient,
			)
			if err != nil {
				panic(err)
			}
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

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(), cache.Indexers{
		indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
	})

	return s.registerController(&controllerWrapper{
		Name: extraannotationsync.ControllerName,
		Runner: func(ctx context.Context) {
			c, err := extraannotationsync.NewController(kcpClusterClient,
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports(),
				s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings(),
			)
			if err != nil {
				panic(err)
			}
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

	if err := s.registerController(&controllerWrapper{
		Name: kubequota.ControllerName,
		Runner: func(ctx context.Context) {
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
				panic(err)
			}
			c.Start(ctx, 2)
		},
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

	return s.registerController(&controllerWrapper{
		Name: identitycache.ControllerName,
		Runner: func(ctx context.Context) {
			c, err := identitycache.NewApiExportIdentityProviderController(kubeClusterClient, s.CacheKcpSharedInformerFactory.Apis().V1alpha1().APIExports(), s.KubeSharedInformerFactory.Core().V1().ConfigMaps())
			if err != nil {
				panic(err)
			}
			c.Start(ctx, 1)
		},
	})
}

func (s *Server) installReplicationController(ctx context.Context, config *rest.Config) error {
	localKcpInformers := s.KcpSharedInformerFactory
	globalKcpInformers := s.CacheKcpSharedInformerFactory
	localKubeInformers := s.KubeSharedInformerFactory
	globalKubeInformers := s.CacheKubeSharedInformerFactory

	gvrs := map[schema.GroupVersionResource]replication.ReplicatedGVR{
		apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"): {
			Kind:   "APIExport",
			Local:  localKcpInformers.Apis().V1alpha1().APIExports().Informer(),
			Global: globalKcpInformers.Apis().V1alpha1().APIExports().Informer(),
		},
		apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"): {
			Kind:   "APIResourceSchema",
			Local:  localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			Global: globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
		},
		apisv1alpha1.SchemeGroupVersion.WithResource("apiconversions"): {
			Kind:   "APIConversion",
			Local:  localKcpInformers.Apis().V1alpha1().APIConversions().Informer(),
			Global: globalKcpInformers.Apis().V1alpha1().APIConversions().Informer(),
		},
		admissionregistrationv1.SchemeGroupVersion.WithResource("mutatingwebhookconfigurations"): {
			Kind:   "MutatingWebhookConfiguration",
			Local:  localKubeInformers.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
			Global: globalKubeInformers.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
		},
		admissionregistrationv1.SchemeGroupVersion.WithResource("validatingwebhookconfigurations"): {
			Kind:   "ValidatingWebhookConfiguration",
			Local:  localKubeInformers.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
			Global: globalKubeInformers.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
		},
		admissionregistrationv1alpha1.SchemeGroupVersion.WithResource("validatingadmissionpolicies"): {
			Kind:   "ValidatingAdmissionPolicy",
			Local:  localKubeInformers.Admissionregistration().V1alpha1().ValidatingAdmissionPolicies().Informer(),
			Global: globalKubeInformers.Admissionregistration().V1alpha1().ValidatingAdmissionPolicies().Informer(),
		},
		admissionregistrationv1alpha1.SchemeGroupVersion.WithResource("validatingadmissionpolicybindings"): {
			Kind:   "ValidatingAdmissionPolicyBinding",
			Local:  localKubeInformers.Admissionregistration().V1alpha1().ValidatingAdmissionPolicyBindings().Informer(),
			Global: globalKubeInformers.Admissionregistration().V1alpha1().ValidatingAdmissionPolicyBindings().Informer(),
		},
		corev1alpha1.SchemeGroupVersion.WithResource("shards"): {
			Kind:   "Shard",
			Local:  localKcpInformers.Core().V1alpha1().Shards().Informer(),
			Global: globalKcpInformers.Core().V1alpha1().Shards().Informer(),
		},
		corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters"): {
			Kind: "LogicalCluster",
			Filter: func(u *unstructured.Unstructured) bool {
				return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
			},
			Local:  localKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
			Global: globalKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
		},
		tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"): {
			Kind:   "WorkspaceType",
			Local:  localKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
			Global: globalKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
		},
		rbacv1.SchemeGroupVersion.WithResource("clusterroles"): {
			Kind: "ClusterRole",
			Filter: func(u *unstructured.Unstructured) bool {
				return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
			},
			Local:  localKubeInformers.Rbac().V1().ClusterRoles().Informer(),
			Global: globalKubeInformers.Rbac().V1().ClusterRoles().Informer(),
		},
		rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"): {
			Kind: "ClusterRoleBinding",
			Filter: func(u *unstructured.Unstructured) bool {
				return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
			},
			Local:  localKubeInformers.Rbac().V1().ClusterRoleBindings().Informer(),
			Global: globalKubeInformers.Rbac().V1().ClusterRoleBindings().Informer(),
		},
	}

	for _, controller := range gvrs {
		indexers.AddIfNotPresentOrDie(
			controller.Global.GetIndexer(),
			cache.Indexers{
				replication.ByShardAndLogicalClusterAndNamespaceAndName: replication.IndexByShardAndLogicalClusterAndNamespace,
			},
		)
	}
	return s.registerController(&controllerWrapper{
		Name: replication.ControllerName,
		Wait: func(ctx context.Context, s *Server) error {
			return wait.PollUntilContextCancel(ctx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
				for _, controller := range gvrs {
					globalSynced := controller.Global.HasSynced()
					localSynced := controller.Local.HasSynced()
					if !globalSynced || !localSynced {
						return false, nil
					}
				}
				return true, nil
			})
		},
		Runner: func(ctx context.Context) {
			// TODO(sttts): set user agent
			controller, err := replication.NewController(
				s.Options.Extra.ShardName,
				s.CacheDynamicClient,
				s.KcpSharedInformerFactory,
				s.CacheKcpSharedInformerFactory,
				s.KubeSharedInformerFactory,
				s.CacheKubeSharedInformerFactory,
				gvrs,
			)
			if err != nil {
				panic(err)
			}
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

	return s.registerController(&controllerWrapper{
		Name: garbagecollector.ControllerName,
		Runner: func(ctx context.Context) {
			c, err := garbagecollector.NewController(
				s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters(),
				kubeClusterClient,
				metadataClient,
				s.DiscoveringDynamicSharedInformerFactory,
				workersPerLogicalCluster,
				s.syncedCh,
			)
			if err != nil {
				panic(err)
			}
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
