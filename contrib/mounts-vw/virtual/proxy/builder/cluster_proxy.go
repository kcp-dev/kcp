package builder

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/davecgh/go-spew/spew"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	mountsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/mounts/v1alpha1"
)

func (p *clusterProxy) authorizeCluster(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
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
		Verb:            "proxy",
		Name:            proxyName,
		APIGroup:        mountsv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      mountsv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusters",
		ResourceRequest: true,
	}

	return authz.Authorize(ctx, SARAttributes)
}

func (p *clusterProxy) resolveClusterProxyRootPath(urlPath string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	select {
	case <-p.readyClusterCh:
	default:
		// If the cluster is not ready, we should not accept any requests
		fmt.Println("VirtualWorkspace is not ready")
		return
	}

	rootPathPrefix := p.rootPathPrefix + p.clusterParameters.virtualWorkspaceName + "/"
	spew.Dump(rootPathPrefix)
	completedContext = requestContext
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return
	}
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	// Incoming requests to this virtual workspace will look like:
	//  /services/cluster-proxy/root:org:ws/apis/targets.contrib.kcp.io/v1alpha1/kubeclusters/<kube-cluster-name>/core/v1/namespaces/<namespace>/pods/<pod>/exec
	//                                  └───────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	parts := strings.SplitN(withoutRootPathPrefix, "/", 7)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return
	}
	clusterPath := logicalcluster.NewPath(parts[0])
	proxyName := parts[5]

	clusterName, ok := clusterPath.Name()
	if !ok {
		return
	}

	// TODO: verify proxy exists here
	apiDomainKey := dynamiccontext.APIDomainKey(kcpcache.ToClusterAwareKey(clusterName.String(), "", proxyName))

	//  /services/cluster-proxy/<hash>/targets.contrib.kcp.io/v1alpha1/kubeclusters/<kube-cluster-name>/core/v1/namespaces/<namespace>/pods/<pod>/exec
	//                  ┌──────────────────────────────────────────────────────────────────────────────────┘
	// We are now here: ┘

	completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomainKey)
	completedContext = genericapirequest.WithCluster(completedContext, genericapirequest.Cluster{
		Name: clusterName,
	})

	prefixToStrip = path.Join(rootPathPrefix, parts[0])
	accepted = true
	return
}
