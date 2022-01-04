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

package fixedgvs

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	restStorage "k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
)

type RestStorageBuilder func(apiGroupAPIServerConfig genericapiserver.CompletedConfig) (restStorage.Storage, error)

type GroupVersionAPISet struct {
	GroupVersion           schema.GroupVersion
	AddToScheme            func(*runtime.Scheme) error
	BootstrapRestResources func(rootAPIServerConfig genericapiserver.CompletedConfig) (map[string]RestStorageBuilder, error)
}

type FixedGroupVersionsVirtualWorkspace struct {
	Name                string
	RootPathResolver    framework.RootPathResolverFunc
	Ready               framework.ReadyFunc
	GroupVersionAPISets []GroupVersionAPISet
}

func (vw *FixedGroupVersionsVirtualWorkspace) GetName() string {
	return vw.Name
}

func (vw *FixedGroupVersionsVirtualWorkspace) IsReady() error {
	return vw.Ready()
}

func (vw *FixedGroupVersionsVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	return vw.RootPathResolver(urlPath, context)
}
