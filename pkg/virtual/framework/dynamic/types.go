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
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
)

var _ framework.VirtualWorkspace = (*DynamicVirtualWorkspace)(nil)

// DynamicVirtualWorkspace is an implementation of a framework.VirtualWorkspace which can dynamically serve resources,
// based on API definitions (including an OpenAPI v3 schema), and a Rest storage provider.
type DynamicVirtualWorkspace struct {
	framework.RootPathResolver
	authorizer.Authorizer
	framework.ReadyChecker

	// BootstrapAPISetManagement creates, initializes and returns an apidefinition.APIDefinitionSetGetter.
	// Usually it would also set up some logic that will call the apiserver.CreateServingInfoFor() method
	// to add an apidefinition.APIDefinition in the apidefinition.APIDefinitionSetGetter on some event.
	BootstrapAPISetManagement func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error)
}
