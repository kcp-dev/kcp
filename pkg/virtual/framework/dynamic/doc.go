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

// Package dynamic provides the types and underlying implementation
// required to build virtual workspaces which can dynamically serve resources,
// based on API definitions (including an OpenAPI v3 schema), and a Rest storage provider.
//
// # Main idea
//
// APIs served by the virtual workspace are partitioned in API domains. Each API domain
// is defined by an api domain key and will expose a set of APIs
// that will be served at a dedicated URL path. API domain key is generally extracted from the URL sub-path
// where related APIs will be exposed.
//
// APIs are served by an apiserver.DynamicAPIServer which is setup in the virtual workspace Register() method.
//
// # Typical operation
//
// The RootPathResolver() method would usually interpret the URL sub-path, extract the API domain key from it,
// and pass the request to the apiserver.DynamicAPIServer, along with the API domain key, if the API domain key contains APIs.
//
// The BootstrapAPISetManagement() method would typically create and initialize an apidefinition.APIDefinitionSetGetter, returned as a result,
// and setup some logic that will call the apiserver.CreateServingInfoFor() method to add an APIDefinition
// in the APIDefinitionSetGetter on some event.
//
// The apidefinition.APIDefinitionSetGetter returned by the BootstrapAPISetManagement() method is passed to the apiserver.DynamicAPIServer during the Register() call.
// The apiserver.DyncamicAPIServer uses it to correctly choose the appropriate apidefinition.APIDefinition used to serve the request,
// based on the context api domain key and the requested API GVR.
package dynamic
