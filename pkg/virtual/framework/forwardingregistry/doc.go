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

// Package forwardingregistry provides a CRD-like REST storage implementation that can dynamically serve resources based
// on a given OpenAPI schema, and forward the requests to a KCP workspace-aware delegate client.
//
// It reuses as much as possible from k8s.io/apiextensions-apiserver/pkg/registry/customresource, but
// replaces the underlying Store, using forwarding rather than access to etcd via genericregistry.Store.
package forwardingregistry
