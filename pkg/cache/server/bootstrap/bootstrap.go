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

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// SystemCRDLogicalCluster holds a logical cluster name under which we store system-related CRDs.
// We use the same name as the KCP for symmetry.
var SystemCRDLogicalCluster = logicalcluster.New("system:system-crds")

func Bootstrap(ctx context.Context, apiExtensionsClusterClient apiextensionsclient.ClusterInterface) error {
	crds := []*apiextensionsv1.CustomResourceDefinition{}
	for _, resource := range []string{"apiresourceschemas", "apiexports"} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := configcrds.Unmarshal(fmt.Sprintf("apis.kcp.dev_%s.yaml", resource), crd); err != nil {
			panic(fmt.Errorf("failed to unmarshal %v resource: %w", resource, err))
		}
		for i := range crd.Spec.Versions {
			v := &crd.Spec.Versions[i]
			v.Schema = &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:                   "object",
					XPreserveUnknownFields: pointer.BoolPtr(true),
				},
			} // wipe the schema, we don't need validation
		}
		crds = append(crds, crd)
	}

	logger := klog.FromContext(ctx)
	return wait.PollInfiniteWithContext(ctx, time.Second, func(ctx context.Context) (bool, error) {
		for _, crd := range crds {
			err := configcrds.CreateSingle(ctx, apiExtensionsClusterClient.Cluster(SystemCRDLogicalCluster).ApiextensionsV1().CustomResourceDefinitions(), crd)
			if err != nil {
				logging.WithObject(logger, crd).Error(err, "failed to create CustomResourceDefinition")
				return false, nil
			}
		}
		return true, nil
	})
}
