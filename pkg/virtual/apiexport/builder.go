package apiexport

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/kcp-dev/logicalcluster"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clusters"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const VirtualWorkspaceName string = "apiexport"

func NewVirtualWorkspace(
	rootPathPrefix string,
	dynamicClusterClient dynamic.ClusterInterface,
	apiExportInformer apisinformers.APIExportInformer,
	apiResourceSchemaInformer apisinformers.APIResourceSchemaInformer,
) framework.VirtualWorkspace {
	virtualWorkspacePathPrefix := path.Join(rootPathPrefix, VirtualWorkspaceName)

	if !strings.HasSuffix(virtualWorkspacePathPrefix, "/") {
		virtualWorkspacePathPrefix += "/"
	}

	return &virtualdynamic.DynamicVirtualWorkspace{
		Name: VirtualWorkspaceName,

		RootPathResolver: func(path string, requestContext context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			completedContext = requestContext

			if !strings.HasPrefix(path, virtualWorkspacePathPrefix) {
				return
			}

			// /services/apiexport/$cluster/$apiExport/$identity/clusters/...

			withoutRootPathPrefix := strings.TrimPrefix(path, virtualWorkspacePathPrefix)

			parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
			if len(parts) < 3 {
				return
			}

			apiExportClusterName, apiExportName, identityHash := parts[0], parts[1], parts[2]
			if apiExportClusterName == "" {
				return
			}
			if apiExportName == "" {
				return
			}
			if identityHash == "" {
				return
			}

			realPath := "/"
			if len(parts) > 3 {
				realPath += parts[3]
			}

			cluster := genericapirequest.Cluster{Name: logicalcluster.Wildcard, Wildcard: true}
			if strings.HasPrefix(realPath, "/clusters/") {
				withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
				parts = strings.SplitN(withoutClustersPrefix, "/", 2)

				clusterName := parts[0]
				realPath = "/"
				if len(parts) > 1 {
					realPath += parts[1]
				}
				cluster = genericapirequest.Cluster{Name: logicalcluster.New(clusterName)}
				if clusterName == "*" {
					cluster.Wildcard = true
				}
			}

			completedContext = genericapirequest.WithCluster(requestContext, cluster)
			key := fmt.Sprintf("%s/%s/%s", apiExportClusterName, apiExportName, identityHash)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, dynamiccontext.APIDomainKey(key))

			prefixToStrip = strings.TrimSuffix(path, realPath)
			accepted = true

			return
		},

		Ready: func() error {
			return nil
		},

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &apiSetRetriever{
				genericConfig:        mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
					return apiExportInformer.Lister().Get(clusters.ToClusterAwareKey(clusterName, name))
				},
				getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return apiResourceSchemaInformer.Lister().Get(clusters.ToClusterAwareKey(clusterName, name))
				},
			}, nil
		},
	}
}
