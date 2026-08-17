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
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

func obj(name string, objLabels map[string]string) unstructured.Unstructured {
	u := unstructured.Unstructured{Object: map[string]interface{}{}}
	u.SetName(name)
	if objLabels != nil {
		u.SetLabels(objLabels)
	}
	return u
}

func names(items []unstructured.Unstructured) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.GetName())
	}
	return out
}

func TestSelectionMatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		spec      cachev1alpha1.ClusterCachedResourceSpec
		object    unstructured.Unstructured
		wantMatch bool
	}{
		{
			name:      "empty selection publishes the whole kind",
			spec:      cachev1alpha1.ClusterCachedResourceSpec{},
			object:    obj("anything", nil),
			wantMatch: true,
		},
		{
			name: "label selector matches",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
			},
			object:    obj("one", map[string]string{"app": "sheriff", "extra": "fine"}),
			wantMatch: true,
		},
		{
			name: "label selector rejects",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
			},
			object:    obj("one", map[string]string{"app": "outlaw"}),
			wantMatch: false,
		},
		{
			name: "names match",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				Names: []string{"one", "two"},
			},
			object:    obj("two", nil),
			wantMatch: true,
		},
		{
			name: "names reject",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				Names: []string{"one", "two"},
			},
			object:    obj("three", nil),
			wantMatch: false,
		},
		{
			name: "both halves have to hold, name alone is not enough",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
				Names:         []string{"one"},
			},
			object:    obj("one", map[string]string{"app": "outlaw"}),
			wantMatch: false,
		},
		{
			name: "both halves have to hold, labels alone are not enough",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
				Names:         []string{"one"},
			},
			object:    obj("two", map[string]string{"app": "sheriff"}),
			wantMatch: false,
		},
		{
			name: "both halves hold",
			spec: cachev1alpha1.ClusterCachedResourceSpec{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
				Names:         []string{"one"},
			},
			object:    obj("one", map[string]string{"app": "sheriff"}),
			wantMatch: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := SelectionFor(&cachev1alpha1.ClusterCachedResource{Spec: tc.spec})
			require.Equal(t, tc.wantMatch, s.Matches(&tc.object))
		})
	}
}

func TestSelectionListOptions(t *testing.T) {
	t.Parallel()
	empty := SelectionFor(&cachev1alpha1.ClusterCachedResource{})
	require.Empty(t, empty.ListOptions().LabelSelector)

	withLabels := SelectionFor(&cachev1alpha1.ClusterCachedResource{
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
			// Names deliberately have no ListOptions equivalent: a field
			// selector compares one value and a selection may name several.
			Names: []string{"one"},
		},
	})
	require.Equal(t, "app=sheriff", withLabels.ListOptions().LabelSelector)
}

func TestSelectionFilter(t *testing.T) {
	t.Parallel()
	items := []unstructured.Unstructured{
		obj("one", map[string]string{"app": "sheriff"}),
		obj("two", map[string]string{"app": "outlaw"}),
		obj("three", nil),
	}

	empty := SelectionFor(&cachev1alpha1.ClusterCachedResource{})
	require.Equal(t, []string{"one", "two", "three"}, names(empty.Filter(items)))

	s := SelectionFor(&cachev1alpha1.ClusterCachedResource{
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sheriff"}},
			Names:         []string{"one", "three"},
		},
	})
	require.Equal(t, []string{"one"}, names(s.Filter(items)))

	// Filtering must not disturb the list it was given.
	require.Equal(t, []string{"one", "two", "three"}, names(items))
}
