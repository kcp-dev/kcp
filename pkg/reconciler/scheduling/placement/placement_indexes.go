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

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
)

func indexByWorksapce(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	lcluster := logicalcluster.From(metaObj)
	return []string{lcluster.String()}, nil
}

const indexUnscheduledNamedspacesKey = "unscheduled"

func indexUnscheduledNamespaces(obj interface{}) ([]string, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an Namespace, but is %T", obj)
	}

	if _, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]; !found {
		return []string{indexUnscheduledNamedspacesKey}, nil
	}
	return []string{}, nil
}

func indexUnscheduledByWorkspace(obj interface{}) ([]string, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an Namespace, but is %T", obj)
	}

	if _, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]; !found {
		return []string{logicalcluster.From(ns).String()}, nil
	}
	return []string{}, nil
}
