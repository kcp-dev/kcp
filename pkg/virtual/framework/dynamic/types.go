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

package dynamic

import (
	"context"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
)

var _ framework.VirtualWorkspace = (*DynamicVirtualWorkspace)(nil)

// DynamicVirtualWorkspace is an implemntation of a VirtualWorkspace which can dynamically serve resources,
// based on API definitions (including an OpenAPI v3 schema), and a Rest storage provider.
type DynamicVirtualWorkspace struct {
	Name             string
	RootPathResolver framework.RootPathResolverFunc
	Ready            framework.ReadyFunc

	// BootstrapAPISetManagement creates, initializes and returns an apidefs.APISetRetriever.
	// Usually it would also setup some logic that will call the apiserver.CreateServingInfoFor() method
	// to add an apidefs.APIDefinition in the apidefs.APISetRetriever on some event.
	BootstrapAPISetManagement func(mainConfig genericapiserver.CompletedConfig) (apidefs.APIDefinitionSetGetter, error)
}

func (vw *DynamicVirtualWorkspace) GetName() string {
	return vw.Name
}

func (vw *DynamicVirtualWorkspace) IsReady() error {
	return vw.Ready()
}

func (vw *DynamicVirtualWorkspace) ResolveRootPath(urlPath string, context context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
	return vw.RootPathResolver(urlPath, context)
}
