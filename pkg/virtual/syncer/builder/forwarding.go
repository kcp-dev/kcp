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

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/kube-openapi/pkg/validation/validate"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	registry "github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
)

var _ registry.ClientGetter = (*clusterAwareClientGetter)(nil)

type clusterAwareClientGetter struct {
	clusterInterface dynamic.ClusterInterface
}

func (g *clusterAwareClientGetter) GetDynamicClient(ctx context.Context) (dynamic.Interface, error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return nil, err
	}
	if cluster.Wildcard {
		return g.clusterInterface.Cluster(logicalcluster.Wildcard), nil
	} else {
		return g.clusterInterface.Cluster(cluster.Name), nil
	}
}

func provideForwardingRestStorage(dynamicClientGetter registry.ClientGetter) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator *validate.SchemaValidator, subresourcesSchemaValidator map[string]*validate.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		statusSchemaValidate, statusEnabled := subresourcesSchemaValidator["status"]

		var statusSpec *apiextensions.CustomResourceSubresourceStatus
		if statusEnabled {
			statusSpec = &apiextensions.CustomResourceSubresourceStatus{}
		}

		var scaleSpec *apiextensions.CustomResourceSubresourceScale
		// TODO(sttts): implement scale subresource

		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			schemaValidator,
			statusSchemaValidate,
			map[string]*structuralschema.Structural{resource.Version: structuralSchema},
			statusSpec,
			scaleSpec,
		)

		storage := registry.NewStorage(
			resource,
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			dynamicClientGetter,
			nil,
		)

		subresourceStorages = make(map[string]rest.Storage)
		if statusEnabled {
			subresourceStorages["status"] = storage.Status
		}

		// TODO(sttts): add scale subresource

		return storage.CustomResource, subresourceStorages
	}
}
