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

// Package forwardingregistry provides a CRD-like REST storage implementation that can dynamically serve resources based
// on a given OpenAPI schema, and forward the requests to a KCP workspace-aware delegate client.
//
// Parts of this package are highly inspired from k8s.io/kubernetes/staging/src/k8s.io/apiextensions-apiserver/pkg/apiserver
// https://github.com/kcp-dev/kubernetes/tree/feature-logical-clusters-1.23/staging/src/k8s.io/apiextensions-apiserver/pkg/registry
package forwardingregistry
