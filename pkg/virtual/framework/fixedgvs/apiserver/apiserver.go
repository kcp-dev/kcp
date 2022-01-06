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
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/registry/rest"
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
		if vwName := r.Context().Value(virtualcontext.VirtualWorkspaceNameKey); vwName != nil {
			if vwNameString, isString := vwName.(string); isString && vwNameString == virtualWorkspaceName {
				director.ServeHTTP(rw, r)
				return
			}
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

	storage := map[string]rest.Storage{}
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
