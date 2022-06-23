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

package apibinder

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/client-go/tools/clusters"

	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
)

func indexByExport(obj interface{}) ([]string, error) {
	apiSet, ok := obj.(*initializationv1alpha1.APISet)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a initializationv1alpha1.APISet, but is %T", obj)
	}

	var exports []string
	for _, binding := range apiSet.Spec.Bindings {
		if workspace := binding.Reference.Workspace; workspace != nil {
			exports = append(exports, clusters.ToClusterAwareKey(logicalcluster.New(workspace.Path), workspace.ExportName))
		}
	}
	return exports, nil
}
