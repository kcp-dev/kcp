package builder

import (
	"context"
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
)

func (p *clusterProxy) authorizeEdge(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	proxyKey := dynamiccontext.APIDomainKeyFrom(ctx)
	negotiationWorkspaceName, _, proxyName, err := kcpcache.SplitMetaClusterNamespaceKey(string(proxyKey))
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	authz, err := delegated.NewDelegatedAuthorizer(negotiationWorkspaceName, p.kubeClusterClient, delegated.Options{})
	if err != nil {
		return authorizer.DecisionNoOpinion, "Error", err
	}

	SARAttributes := authorizer.AttributesRecord{
		User:            a.GetUser(),
		Verb:            "tunnel",
		Name:            proxyName,
		APIGroup:        proxyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      proxyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "workspaceproxies",
		ResourceRequest: true,
	}

	return authz.Authorize(ctx, SARAttributes)
}

func (p *clusterProxy) resolveEdgeRootPath(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	select {
	case <-p.readyEdgeCh:
	default:
		return
	}
	//logger := klog.FromContext(requestContext)

	rootPathPrefix := p.rootPathPrefix + p.edgeParameters.virtualWorkspaceName + "/"

	completedContext = requestContext
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/edge-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/tunnel
	//                                  └───────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return
	}
	path := logicalcluster.NewPath(parts[0])
	proxyName := parts[1]
	//	proxyUID := parts[2]

	clusterName, ok := path.Name()
	if !ok {
		return
	}

	apiDomainKey := dynamiccontext.APIDomainKey(kcpcache.ToClusterAwareKey(clusterName.String(), "", proxyName))

	realPath := "/"
	if len(parts) > 3 {
		realPath += parts[3]
	}

	//  /services/edge-proxy/root:org:ws/<proxy-target-name>/<proxy-target-uid>/tunnel
	//                  ┌────────────────────────────────────────────────────┘
	// We are now here: ┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/tunnel") {
		return // don't accept
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/tunnel")
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

	prefixToStrip = strings.TrimSuffix(urlPath, realPath)

	// These are needed for authorization to work too.
	completedContext = genericapirequest.WithCluster(requestContext, cluster)
	completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
	// TODO: this is wrong place
	completedContext = genericapirequest.WithRequestInfo(completedContext, &genericapirequest.RequestInfo{
		IsResourceRequest: true,
		Verb:              "tunnel",
		Resource:          "workspaceproxies",
		APIGroup:          proxyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:        proxyv1alpha1.SchemeGroupVersion.Version,
		Name:              proxyName,
		Namespace:         "",
	})

	accepted = true
	return
}

// getDialer returns a reverse dialer for the id.
func (p *clusterProxy) getDialer(clusterName logicalcluster.Name, proxyName string) *Dialer {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := key{clusterName, proxyName}
	return p.pool[id]
}
