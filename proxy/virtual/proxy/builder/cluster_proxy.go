package builder

import (
	"context"
	"fmt"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxycontext "github.com/kcp-dev/kcp/proxy/virtual/context"
)

func (p *clusterProxy) authorizeCluster(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	syncTargetKey := dynamiccontext.APIDomainKeyFrom(ctx)
	negotiationWorkspaceName, _, syncTargetName, err := kcpcache.SplitMetaClusterNamespaceKey(string(syncTargetKey))
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	authz, err := delegated.NewDelegatedAuthorizer(negotiationWorkspaceName, p.kubeClusterClient, delegated.Options{})
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

func (p *clusterProxy) resolveClusterProxyRootPath(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	select {
	case <-p.readyClusterCh:
	default:
		return
	}

	rootPathPrefix := p.rootPathPrefix + p.clusterParameters.virtualWorkspaceName + "/"
	completedContext = requestContext
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/cluster-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/clusters/*/core/v1/namespaces/<namespace>/pods/<pod>/exec
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
	proxyTarget, err := p.cachedProxyInformers.Proxy().V1alpha1().WorkspaceProxies().Cluster(clusterName).Lister().Get(proxyTargetName)
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

	//  /services/cluster-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/clusters/*/core/v1/namespaces/<namespace>/pods/<pod>/exec
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
