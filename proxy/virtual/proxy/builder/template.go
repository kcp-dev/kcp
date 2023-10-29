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

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxyinformers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions"
	proxycontext "github.com/kcp-dev/kcp/proxy/virtual/context"
)

type templateProvider struct {
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient kcpdynamic.ClusterInterface
	cachedProxyInformers proxyinformers.SharedInformerFactory
	rootPathPrefix       string
}

type templateParameters struct {
	virtualWorkspaceName string
}

func (p *templateProvider) newTemplate(parameters templateParameters) *template {
	return &template{
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
	//  /services/cluster-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/clusters/*/api/v1/configmaps
	//                                  └───────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return
	}
	path := logicalcluster.NewPath(parts[0])
	proxyTargetName := parts[1]
	proxyTargetUID := parts[2]

	clusterName, ok := path.Name()
	if !ok {
		return
	}

	apiDomainKey := dynamiccontext.APIDomainKey(kcpcache.ToClusterAwareKey(clusterName.String(), "", proxyTargetName))

	// In order to avoid conflicts with reusing deleted proxy names, let's make sure that the proxy name and proxy UID match, if not,
	// that likely means that a proxy is running with a stale proxy that got deleted.
	proxyTarget, err := t.cachedProxyInformers.Proxy().V1alpha1().Clusters().Cluster(clusterName).Lister().Get(proxyTargetName)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to get proxy %s|%s: %w", path, proxyTargetName, err))
		return
	}
	if string(proxyTarget.UID) != proxyTargetUID {
		utilruntime.HandleError(fmt.Errorf("proxy target UID mismatch: %s != %s", proxyTarget.UID, proxyTargetUID))
		return
	}

	realPath := "/"
	if len(parts) > 3 {
		realPath += parts[3]
	}

	//  /services/cluster-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/clusters/*/api/v1/configmaps
	//                  ┌────────────────────────────────────────────────────┘
	// We are now here: ┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		return // don't accept
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	reqPath := logicalcluster.NewPath(parts[0])
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

	proxyTargetKey := proxyv1alpha1.ToProxyTargetKey(clusterName, proxyTargetName)
	completedContext = genericapirequest.WithCluster(requestContext, cluster)
	completedContext = proxycontext.WithProxyTargetKey(completedContext, proxyTargetKey)
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
		return errors.New("proxy virtual workspace controllers are not started")
	}
}

func (t *template) authorize(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	syncTargetKey := dynamiccontext.APIDomainKeyFrom(ctx)
	negotiationWorkspaceName, _, syncTargetName, err := kcpcache.SplitMetaClusterNamespaceKey(string(syncTargetKey))
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	authz, err := delegated.NewDelegatedAuthorizer(negotiationWorkspaceName, t.kubeClusterClient, delegated.Options{})
	if err != nil {
		return authorizer.DecisionNoOpinion, "Error", err
	}
	SARAttributes := authorizer.AttributesRecord{
		User:            a.GetUser(),
		Verb:            "proxy",
		Name:            syncTargetName,
		APIGroup:        proxyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      proxyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusters",
		ResourceRequest: true,
	}
	return authz.Authorize(ctx, SARAttributes)
}

func (t *template) bootstrapManagement(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
	// TBC
	return nil, errors.New("not implemented")
}

func (t *template) buildVirtualWorkspace() *virtualworkspacesdynamic.DynamicVirtualWorkspace {
	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver:          framework.RootPathResolverFunc(t.resolveRootPath),
		Authorizer:                authorizer.AuthorizerFunc(t.authorize),
		ReadyChecker:              framework.ReadyFunc(t.ready),
		BootstrapAPISetManagement: t.bootstrapManagement,
	}
}
