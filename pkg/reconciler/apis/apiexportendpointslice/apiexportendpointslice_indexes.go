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

package apiexportendpointslice

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

const indexAPIExportEndpointSliceByAPIExport = "indexAPIExportEndpointSliceByAPIExport"

// indexAPIExportEndpointSliceByAPIExportFunc indexes the APIExportEndpointSlice by their APIExport's Reference Path and Name.
func indexAPIExportEndpointSliceByAPIExportFunc(obj interface{}) ([]string, error) {
	apiExportEndpointSlice, ok := obj.(*apisv1alpha1.APIExportEndpointSlice)
	if !ok {
		return []string{}, fmt.Errorf("obj %T is not an APIExportEndpointSlice", obj)
	}

	path := logicalcluster.NewPath(apiExportEndpointSlice.Spec.APIExport.Path)
	if path.Empty() {
		path = logicalcluster.From(apiExportEndpointSlice).Path()
	}
	return []string{path.Join(apiExportEndpointSlice.Spec.APIExport.Name).String()}, nil
}
