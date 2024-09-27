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

package indexers

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// ClusterAndGroupResourceValue returns the index value for use with
// IndexAPIBindingByClusterAndAcceptedClaimedGroupResources from clusterName and groupResource.
func ClusterAndGroupResourceValue(clusterName logicalcluster.Name, groupResource schema.GroupResource) string {
	return fmt.Sprintf("%s|%s", clusterName, groupResource)
}

// IndexAPIBindingByClusterAndAcceptedClaimedGroupResources is an index function that indexes an APIBinding by its
// accepted permission claims' group resources.
func IndexAPIBindingByClusterAndAcceptedClaimedGroupResources(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Spec.PermissionClaims))
	for _, c := range apiBinding.Spec.PermissionClaims {
		if c.State != apisv1alpha1.ClaimAccepted {
			continue
		}

		groupResource := schema.GroupResource{Group: c.Group, Resource: c.Resource}
		ret = append(ret, ClusterAndGroupResourceValue(logicalcluster.From(apiBinding), groupResource))
	}

	return ret, nil
}

const APIBindingByBoundResourceUID = "byBoundResourceUID"

func IndexAPIBindingByBoundResourceUID(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))
	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, r.Schema.UID)
	}

	return ret, nil
}

const APIBindingByBoundResources = "byBoundResources"

func IndexAPIBindingByBoundResources(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	clusterName := logicalcluster.From(apiBinding)

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))
	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, APIBindingBoundResourceValue(clusterName, r.Group, r.Resource))
	}

	return ret, nil
}

func APIBindingBoundResourceValue(clusterName logicalcluster.Name, group, resource string) string {
	return fmt.Sprintf("%s|%s.%s", clusterName, resource, group)
}

const APIBindingsByAPIExport = "APIBindingByAPIExport"

// IndexAPIBindingByAPIExport indexes the APIBindings by their APIExport's Reference Path and Name.
func IndexAPIBindingByAPIExport(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if path.Empty() {
		path = logicalcluster.From(apiBinding).Path()
	}

	return []string{path.Join(apiBinding.Spec.Reference.Export.Name).String()}, nil
}

const APIBindingsByAPIExportCluster = "APIBindingsByAPIExportCluster"

// IndexAPIBindingsByAPIExportCluster indexes the APIBindings by their APIExport's logical cluster.
func IndexAPIBindingsByAPIExportCluster(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIBinding", obj)
	}

	return []string{APIBindingByBoundResourceValue(logicalcluster.From(apiBinding), logicalcluster.Name(apiBinding.Status.APIExportClusterName))}, nil
}

func APIBindingByBoundResourceValue(clusterName, exportClusterName logicalcluster.Name) string {
	return fmt.Sprintf("%s|%s", clusterName, exportClusterName)
}
