// Package v1alpha1 holds the API types for the Access Virtual
// Workspace (group access.kcp.io).
//
// It exposes a single resource — SelfClusterAccessReview — that
// callers POST against to get back the list of logical clusters they
// can see, along with each cluster's FrontProxy endpoint URL.
//
// +kubebuilder:object:generate=true
// +k8s:openapi-gen=true
// +groupName=access.kcp.io
package v1alpha1
