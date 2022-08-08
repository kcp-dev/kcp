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

package permissionclaim

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// Labeler calculates labels to apply to all instances of a cluster-group-resource based on permission claims.
type Labeler struct {
	listAPIBindingsAcceptingClaimedGroupResource func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha1.APIBinding, error)
	getAPIExport                                 func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error)
}

// NewLabeler returns a new Labeler.
func NewLabeler(
	apiBindingInformer apisinformers.APIBindingInformer,
	apiExportInformer apisinformers.APIExportInformer,
) *Labeler {
	return &Labeler{
		listAPIBindingsAcceptingClaimedGroupResource: func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha1.APIBinding, error) {
			indexKey := indexers.ClusterAndGroupResourceValue(clusterName, groupResource)
			return indexers.ByIndex[*apisv1alpha1.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByClusterAndAcceptedClaimedGroupResources, indexKey)
		},

		getAPIExport: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIExport, error) {
			key := clusters.ToClusterAwareKey(clusterName, name)
			return apiExportInformer.Lister().Get(key)
		},
	}
}

// LabelsFor returns all the applicable labels for the cluster-group-resource relating to permission claims. This is
// the intersection of (1) all APIBindings in the cluster that have accepted claims for the group-resource with (2)
// associated APIExports that are claiming group-resource.
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource) (map[string]string, error) {
	labels := map[string]string{}

	bindings, err := l.listAPIBindingsAcceptingClaimedGroupResource(cluster, groupResource)
	if err != nil {
		return nil, fmt.Errorf("error listing APIBindings in %q accepting claimed group resource %q: %w", cluster, groupResource, err)
	}

	logger := klog.FromContext(ctx)

	for _, binding := range bindings {
		logger := logging.WithObject(logger, binding)

		if binding.Status.BoundAPIExport == nil {
			logger.V(4).Info("skipping APIBinding because it has no bound APIExport")
			continue
		}

		boundAPIExportWorkspace := binding.Status.BoundAPIExport.Workspace
		export, err := l.getAPIExport(logicalcluster.New(boundAPIExportWorkspace.Path), boundAPIExportWorkspace.ExportName)
		if err != nil {
			logger.Error(err, "error getting APIExport", "exportCluster", boundAPIExportWorkspace.Path, "exportName", boundAPIExportWorkspace.ExportName)
			continue
		}

		logger = logging.WithObject(logger, export)

		for _, claim := range export.Spec.PermissionClaims {
			if claim.Group != groupResource.Group || claim.Resource != groupResource.Resource {
				continue
			}

			k, v, err := permissionclaims.ToLabelKeyAndValue(logicalcluster.From(export), export.Name, claim)
			if err != nil {
				// extremely unlikely to get an error here - it means the json marshaling failed
				logger.Error(err, "error calculating permission claim label key and value",
					"claim", claim.String())
				continue
			}
			labels[k] = v
		}
	}

	return labels, nil
}
