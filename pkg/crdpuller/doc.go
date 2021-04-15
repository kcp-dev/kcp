// crdpuller package provides a library to pull API resource definitions
// from existing Kubernetes clusters as Custom Resource Definitions that can then be applied
// to a KCP instance.
//
// - If a CRD already exists for a given resource in the targeted cluster, then it is reused.
// - If no CRD exist in the targeted cluster, then the CRD OpenAPI v3 schema is built
// from the openAPI v2 (Swagger) definitions published by the targeted cluster.
package crdpuller
