/*
Copyright 2021 The KCP Authors.

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

package apiserver

import (
	"errors"
	"net/http"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
)

type ExtraConfig struct {
	GroupVersion    schema.GroupVersion
	StorageBuilders map[string]func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (restStorage.Storage, error)
}

type GroupVersionAPIServerConfig struct {
	GenericConfig *genericapiserver.RecommendedConfig
	ExtraConfig   ExtraConfig
}

// GroupVersionAPIServer contains state for a Kubernetes cluster master/api server.
type GroupVersionAPIServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

type completedConfig struct {
	GenericConfig genericapiserver.CompletedConfig
	ExtraConfig   *ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *GroupVersionAPIServerConfig) Complete() completedConfig {
	cfg := completedConfig{
		c.GenericConfig.Complete(),
		&c.ExtraConfig,
	}

	return cfg
}

// New returns a new instance of VirtualWorkspaceAPIServer from the given config.
func (c completedConfig) New(virtualWorkspaceName string, groupManager discovery.GroupManager, scheme *runtime.Scheme, delegationTarget genericapiserver.DelegationTarget) (*GroupVersionAPIServer, error) {
	genericServer, err := c.GenericConfig.New(virtualWorkspaceName+"-"+c.ExtraConfig.GroupVersion.Group+"-virtual-workspace-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	if groupManager != nil {
		genericServer.DiscoveryGroupManager = groupManager
	}
	director := genericServer.Handler.Director
	genericServer.Handler.Director = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		vwName, found := virtualcontext.VirtualWorkspaceNameFrom(r.Context())
		if !found {
			utilruntime.HandleError(errors.New("context should always contain a virtual workspace name when hitting a virtual workspace delegated APIServer"))
			http.NotFoundHandler().ServeHTTP(rw, r)
			return
		}
		if vwName == virtualWorkspaceName {
			// In the current KCP Kubernetes feature branch, some components (e.g.Discovery index)
			// don't support calls without a cluster set in the request context.
			// That's why we add a dummy cluster name here.
			// However we don't add it for the OpenAPI v2 endpoint since, on the contrary,
			// in our case the OpenAPI Spec will be published by the default OpenAPI Service Provider,
			// which is served when the cluster name is empty.
			if r.URL.Path != "/openapi/v2" {
				context := r.Context()
				context = genericapirequest.WithCluster(context, genericapirequest.Cluster{Name: logicalcluster.New("virtual")})
				r = r.WithContext(context)
			}

			director.ServeHTTP(rw, r)
			return
		}
		delegatedHandler := delegationTarget.UnprotectedHandler()
		if delegatedHandler != nil {
			delegatedHandler.ServeHTTP(rw, r)
		} else {
			http.NotFoundHandler().ServeHTTP(rw, r)
		}
	})

	s := &GroupVersionAPIServer{
		GenericAPIServer: genericServer,
	}

	codecs := serializer.NewCodecFactory(scheme)

	storage := map[string]restStorage.Storage{}
	for resource, storageBuilder := range c.ExtraConfig.StorageBuilders {
		restStorage, err := storageBuilder(c.GenericConfig)
		if err != nil {
			return nil, err
		}
		storage[resource] = restStorage
	}

	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(c.ExtraConfig.GroupVersion.Group, scheme, metav1.ParameterCodec, codecs)
	if len(apiGroupInfo.PrioritizedVersions) == 0 {
		apiGroupInfo.PrioritizedVersions = append(apiGroupInfo.PrioritizedVersions, c.ExtraConfig.GroupVersion)
	}
	apiGroupInfo.VersionedResourcesStorageMap[c.ExtraConfig.GroupVersion.Version] = storage
	if err := s.GenericAPIServer.InstallAPIGroup(&apiGroupInfo); err != nil {
		return nil, err
	}

	return s, nil
}
