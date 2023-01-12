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

package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// SystemCRDLogicalCluster holds a logical cluster name under which we store system-related CRDs.
// We use the same name as the KCP for symmetry.
var SystemCRDLogicalCluster = logicalcluster.Name("system:system-crds")

// SystemCacheServerShard holds a default shard name.
const SystemCacheServerShard = "system:cache:server"

func Bootstrap(ctx context.Context, apiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface) error {
	crds := []*apiextensionsv1.CustomResourceDefinition{}
	for _, gr := range []struct{ group, resource string }{
		{"apis.kcp.io", "apiresourceschemas"},
		{"apis.kcp.io", "apiconversions"},
		{"apis.kcp.io", "apiexports"},
		{"core.kcp.io", "shards"},
		{"tenancy.kcp.io", "workspacetypes"},
		{"workload.kcp.io", "synctargets"},
		{"scheduling.kcp.io", "locations"},
		{"rbac.authorization.k8s.io", "roles"},
		{"rbac.authorization.k8s.io", "clusterroles"},
		{"rbac.authorization.k8s.io", "rolebindings"},
		{"rbac.authorization.k8s.io", "clusterrolebindings"},
	} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := configcrds.Unmarshal(fmt.Sprintf("%s_%s.yaml", gr.group, gr.resource), crd); err != nil {
			panic(fmt.Errorf("failed to unmarshal %v resource: %w", gr, err))
		}
		for i := range crd.Spec.Versions {
			v := &crd.Spec.Versions[i]
			v.Schema = &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:                   "object",
					XPreserveUnknownFields: pointer.BoolPtr(true),
				},
			} // wipe the schema, we don't need validation
			v.Subresources = nil // wipe subresources so that updates don't have to be made against the status endpoint
		}
		crds = append(crds, crd)
	}

	logger := klog.FromContext(ctx)
	ctx = cacheclient.WithShardInContext(ctx, SystemCacheServerShard)
	return wait.PollInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		for _, crd := range crds {
			err := configcrds.CreateSingle(ctx, apiExtensionsClusterClient.Cluster(SystemCRDLogicalCluster.Path()).ApiextensionsV1().CustomResourceDefinitions(), crd)
			if err != nil {
				logging.WithObject(logger, crd).Error(err, "failed to create CustomResourceDefinition")
				return false, nil
			}
		}
		return true, nil
	})
}
