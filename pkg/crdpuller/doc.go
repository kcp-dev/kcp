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

// crdpuller package provides a library to pull API resource definitions
// from existing Kubernetes clusters as Custom Resource Definitions that can then be applied
// to a KCP instance.
//
// - If a CRD already exists for a given resource in the targeted cluster, then it is reused.
// - If no CRD exist in the targeted cluster, then the CRD OpenAPI v3 schema is built
// from the openAPI v2 (Swagger) definitions published by the targeted cluster.
package crdpuller
