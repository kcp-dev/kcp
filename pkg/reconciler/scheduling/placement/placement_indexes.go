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

package placement

import (
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
)

func indexByLocationWorkspace(obj interface{}) ([]string, error) {
	placement, ok := obj.(*schedulingv1alpha1.Placement)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a Placement, but is %T", obj)
	}

	if len(placement.Spec.LocationWorkspace) == 0 {
		return []string{logicalcluster.From(placement).String()}, nil
	}

	return []string{placement.Spec.LocationWorkspace}, nil
}
