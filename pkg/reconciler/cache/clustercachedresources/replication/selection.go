/*
Copyright 2026 The kcp Authors.

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

package replication

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

// Selection is what a ClusterCachedResource says it publishes: everything of
// its kind, or some subset picked out by labels, by name, or by both.
type Selection struct {
	Labels labels.Selector
	Names  sets.Set[string]
}

// SelectionFor reads the selection off a ClusterCachedResource. A resource that
// sets neither publishes its whole kind.
func SelectionFor(clusterCachedResource *cachev1alpha1.ClusterCachedResource) Selection {
	var s Selection
	if clusterCachedResource.Spec.LabelSelector != nil {
		s.Labels = labels.SelectorFromSet(clusterCachedResource.Spec.LabelSelector.MatchLabels)
	}
	if len(clusterCachedResource.Spec.Names) > 0 {
		s.Names = sets.New(clusterCachedResource.Spec.Names...)
	}
	return s
}

// Matches reports whether an object is one of the ones being published. Both
// halves apply: an object has to satisfy every part of the selection that is
// set.
func (s Selection) Matches(obj metav1.Object) bool {
	if s.Labels != nil && !s.Labels.Matches(labels.Set(obj.GetLabels())) {
		return false
	}
	if s.Names.Len() > 0 && !s.Names.Has(obj.GetName()) {
		return false
	}
	return true
}

// ListOptions returns the part of the selection the API server can apply for
// us. Names are left out on purpose -- a field selector only compares one value
// and a selection may name several -- so a caller that lists still has to run
// the results through Matches.
func (s Selection) ListOptions() metav1.ListOptions {
	opts := metav1.ListOptions{}
	if s.Labels != nil {
		opts.LabelSelector = s.Labels.String()
	}
	return opts
}

// Filter drops the objects a selection does not publish.
func (s Selection) Filter(items []unstructured.Unstructured) []unstructured.Unstructured {
	if s.Names.Len() == 0 {
		return items
	}
	out := items[:0]
	for _, item := range items {
		if s.Names.Has(item.GetName()) {
			out = append(out, item)
		}
	}
	return out
}
