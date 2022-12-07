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

package builder

import (
	"context"
	"errors"
	"fmt"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/transforming"
	syncercontext "github.com/kcp-dev/kcp/pkg/virtual/syncer/context"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers/apireconciler"
)

type templateProvider struct {
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient kcpdynamic.ClusterInterface
	kcpClusterClient     kcpclientset.ClusterInterface
	wildcardKcpInformers kcpinformers.SharedInformerFactory
	rootPathPrefix       string
}

type templateParameters struct {
	virtualWorkspaceName string

	filteredResourceState workloadv1alpha1.ResourceState

	restProviderBuilder   BuildRestProviderFunc
	allowedAPIFilter      apireconciler.AllowedAPIfilterFunc
	transformer           transforming.ResourceTransformer
	storageWrapperBuilder func(labels.Requirements) forwardingregistry.StorageWrapper
}

func (p *templateProvider) newTemplate(parameters templateParameters) template {
	return template{
		templateProvider:   *p,
		templateParameters: parameters,
		readyCh:            make(chan struct{}),
	}
}

type template struct {
	templateProvider
	templateParameters

	readyCh chan struct{}
}

func (t *template) resolveRootPath(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	select {
	case <-t.readyCh:
	default:
		return
	}

	rootPathPrefix := t.rootPathPrefix + t.virtualWorkspaceName + "/"
	completedContext = requestContext
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/(up)syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
	//                      └───────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return
	}
	path := logicalcluster.New(parts[0])
	syncTargetName := parts[1]
	syncTargetUID := parts[2]

	clusterName, ok := path.Name()
	if !ok {
		return
	}

	apiDomainKey := dynamiccontext.APIDomainKey(kcpcache.ToClusterAwareKey(clusterName.String(), "", syncTargetName))

	// In order to avoid conflicts with reusing deleted synctarget names, let's make sure that the synctarget name and synctarget UID match, if not,
	// that likely means that a syncer is running with a stale synctarget that got deleted.
	syncTarget, err := t.wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Cluster(clusterName).Lister().Get(syncTargetName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get synctarget %s|%s: %w", path, syncTargetName, err))
		return
	}
	if string(syncTarget.UID) != syncTargetUID {
		utilruntime.HandleError(fmt.Errorf("sync target UID mismatch: %s != %s", syncTarget.UID, syncTargetUID))
		return
	}

	realPath := "/"
	if len(parts) > 3 {
		realPath += parts[3]
	}

	//  /services/(up)syncer/root:org:ws/<sync-target-name>/<sync-target-uid>/clusters/*/api/v1/configmaps
	//                  ┌────────────────────────────────────────────────────┘
	// We are now here: ┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return // don't accept
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	reqPath := logicalcluster.New(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}
	var cluster genericapirequest.Cluster
	if reqPath == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		reqClusterName, ok := reqPath.Name()
		if !ok {
			return
		}
		cluster.Name = reqClusterName
	}

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(clusterName, syncTargetName)
	completedContext = genericapirequest.WithCluster(requestContext, cluster)
	completedContext = syncercontext.WithSyncTargetKey(completedContext, syncTargetKey)
	completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
	prefixToStrip = strings.TrimSuffix(urlPath, realPath)
	accepted = true
	return
}

func (t *template) ready() error {
	select {
	case <-t.readyCh:
		return nil
	default:
		return errors.New("syncer virtual workspace controllers are not started")
	}
}

func (t *template) authorize(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	syncTargetKey := dynamiccontext.APIDomainKeyFrom(ctx)
	negotiationWorkspaceName, _, syncTargetName, err := kcpcache.SplitMetaClusterNamespaceKey(string(syncTargetKey))
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	authz, err := delegated.NewDelegatedAuthorizer(negotiationWorkspaceName, t.kubeClusterClient)
	if err != nil {
		return authorizer.DecisionNoOpinion, "Error", err
	}
	SARAttributes := authorizer.AttributesRecord{
		User:            a.GetUser(),
		Verb:            "sync",
		Name:            syncTargetName,
		APIGroup:        workloadv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      workloadv1alpha1.SchemeGroupVersion.Version,
		Resource:        "synctargets",
		ResourceRequest: true,
	}
	return authz.Authorize(ctx, SARAttributes)
}

func (t *template) bootstrapManagement(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
	apiReconciler, err := apireconciler.NewAPIReconciler(
		t.virtualWorkspaceName,
		t.kcpClusterClient,
		t.wildcardKcpInformers.Workload().V1alpha1().SyncTargets(),
		t.wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas(),
		t.wildcardKcpInformers.Apis().V1alpha1().APIExports(),
		func(syncTargetClusterName logicalcluster.Name, syncTargetName string, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, apiExportIdentityHash string) (apidefinition.APIDefinition, error) {
			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(syncTargetClusterName, syncTargetName)
			requirements, selectable := labels.SelectorFromSet(map[string]string{
				workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: string(t.filteredResourceState),
			}).Requirements()
			if !selectable {
				return nil, fmt.Errorf("unable to build requirements for synctargetkey %s and resource state %s", syncTargetKey, t.filteredResourceState)
			}
			storageWrapper := t.storageWrapperBuilder(requirements)
			transformingClient := t.dynamicClusterClient
			if t.transformer != nil {
				transformingClient = transforming.WithResourceTransformer(t.dynamicClusterClient, t.transformer)
			}
			ctx, cancelFn := context.WithCancel(context.Background())
			storageBuilder := t.restProviderBuilder(ctx, transformingClient, apiExportIdentityHash, storageWrapper)
			def, err := apiserver.CreateServingInfoFor(mainConfig, apiResourceSchema, version, storageBuilder)
			if err != nil {
				cancelFn()
				return nil, err
			}
			return &apiDefinitionWithCancel{
				APIDefinition: def,
				cancelFn:      cancelFn,
			}, nil
		},
		t.allowedAPIFilter,
	)
	if err != nil {
		return nil, err
	}

	if err := mainConfig.AddPostStartHook(apireconciler.ControllerName+t.virtualWorkspaceName, func(hookContext genericapiserver.PostStartHookContext) error {
		defer close(t.readyCh)

		for name, informer := range map[string]cache.SharedIndexInformer{
			"synctargets":        t.wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer(),
			"apiresourceschemas": t.wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			"apiexports":         t.wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer(),
		} {
			if !cache.WaitForNamedCacheSync(name, hookContext.StopCh, informer.HasSynced) {
				klog.Errorf("informer not synced")
				return nil
			}
		}

		go apiReconciler.Start(goContext(hookContext))
		return nil
	}); err != nil {
		return nil, err
	}

	return apiReconciler, nil
}

func (t template) buildVirtualWorkspace() *virtualworkspacesdynamic.DynamicVirtualWorkspace {
	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver:          framework.RootPathResolverFunc(t.resolveRootPath),
		Authorizer:                authorizer.AuthorizerFunc(t.authorize),
		ReadyChecker:              framework.ReadyFunc(t.ready),
		BootstrapAPISetManagement: t.bootstrapManagement,
	}
}

// apiDefinitionWithCancel calls the cancelFn on tear-down.
type apiDefinitionWithCancel struct {
	apidefinition.APIDefinition
	cancelFn func()
}

func (d *apiDefinitionWithCancel) TearDown() {
	d.cancelFn()
	d.APIDefinition.TearDown()
}

func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}
