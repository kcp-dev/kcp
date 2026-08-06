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

package endpointslice

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func selecting(matchLabels map[string]string) corev1alpha1.EndpointSelector {
	return corev1alpha1.EndpointSelector{Selector: &metav1.LabelSelector{MatchLabels: matchLabels}}
}

var everyShard = corev1alpha1.EndpointSelector{MatchAll: true}

// A shard labels itself with its own name when it registers, which is what
// makes "this one shard" expressible without the installation labelling
// anything.
var thisShard = labels.Set{"name": "shard-1", "region": "eu"}

func TestPickURL(t *testing.T) {
	t.Parallel()

	const prefix = "https://shard-1.example.com"

	for _, tc := range []struct {
		name      string
		endpoints []corev1alpha1.Endpoint
		want      string
		wantErr   string
	}{
		{
			name: "no selectors falls back to prefix matching",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://shard-0.example.com/services/a"},
				{URL: "https://shard-1.example.com/services/a"},
			},
			want: "https://shard-1.example.com/services/a",
		},
		{
			name:      "matchAll serves every shard",
			endpoints: []corev1alpha1.Endpoint{{URL: "https://global.example.com/services/a", Shards: everyShard}},
			want:      "https://global.example.com/services/a",
		},
		{
			name:      "a shard can be named",
			endpoints: []corev1alpha1.Endpoint{{URL: "https://one.example.com/services/a", Shards: selecting(map[string]string{"name": "shard-1"})}},
			want:      "https://one.example.com/services/a",
		},
		{
			name:      "a shard that is not named is not served",
			endpoints: []corev1alpha1.Endpoint{{URL: "https://other.example.com/services/a", Shards: selecting(map[string]string{"name": "shard-9"})}},
			wantErr:   "none of the endpoints",
		},
		{
			name: "a region beats the global fallback",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://global.example.com/services/a", Shards: everyShard},
				{URL: "https://eu.example.com/services/a", Shards: selecting(map[string]string{"region": "eu"})},
			},
			want: "https://eu.example.com/services/a",
		},
		{
			name: "a named shard beats its region",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://eu.example.com/services/a", Shards: selecting(map[string]string{"region": "eu"})},
				{URL: "https://one.example.com/services/a", Shards: selecting(map[string]string{"name": "shard-1", "region": "eu"})},
			},
			want: "https://one.example.com/services/a",
		},
		{
			name: "the global fallback still applies outside the region",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://global.example.com/services/a", Shards: everyShard},
				{URL: "https://us.example.com/services/a", Shards: selecting(map[string]string{"region": "us"})},
			},
			want: "https://global.example.com/services/a",
		},
		{
			// The billing case: an APIExport fanned out per shard, and one
			// virtual workspace serving all of them. Saying so overrides the
			// fan-out rather than competing with it.
			name: "a global endpoint overrides per-shard URLs",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://shard-0.example.com/services/a"},
				{URL: "https://shard-1.example.com/services/a"},
				{URL: "https://billing.example.com/services/a", Shards: everyShard},
			},
			want: "https://billing.example.com/services/a",
		},
		{
			// ... and a shard the selectors do not cover is an error rather
			// than a quiet fall back to its own URL, which would send half the
			// installation somewhere the author did not choose.
			name: "a selective endpoint that misses does not fall back to prefix",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://shard-1.example.com/services/a"},
				{URL: "https://us.example.com/services/a", Shards: selecting(map[string]string{"region": "us"})},
			},
			wantErr: "none of the endpoints",
		},
		{
			name: "two equally specific matches is not a guess",
			endpoints: []corev1alpha1.Endpoint{
				{URL: "https://a.example.com/services/a", Shards: selecting(map[string]string{"region": "eu"})},
				{URL: "https://b.example.com/services/a", Shards: selecting(map[string]string{"name": "shard-1"})},
			},
			wantErr: "equally well",
		},
		{
			name: "an expression selector works too",
			endpoints: []corev1alpha1.Endpoint{{
				URL: "https://not-root.example.com/services/a",
				Shards: corev1alpha1.EndpointSelector{Selector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key:      "name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"root"},
					}},
				}},
			}},
			want: "https://not-root.example.com/services/a",
		},
		{
			// The API rejects this, but the reader may still meet it: an
			// endpoint slice of any kind may be written by anyone.
			name: "matchAll together with a selector is not a guess",
			endpoints: []corev1alpha1.Endpoint{{
				URL:    "https://both.example.com/services/a",
				Shards: corev1alpha1.EndpointSelector{MatchAll: true, Selector: &metav1.LabelSelector{}},
			}},
			wantErr: "mutually exclusive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := PickURL(prefix, thisShard, tc.endpoints)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// The endpoint shape is a contract for any kind a reference points at, so it is
// read out of unstructured content rather than a Go type.
func TestListEndpointsFromUnstructured(t *testing.T) {
	t.Parallel()

	slice := unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"endpoints": []interface{}{
				map[string]interface{}{"url": "https://plain.example.com"},
				map[string]interface{}{
					"url": "https://selective.example.com",
					"shards": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{"region": "eu"},
						},
					},
				},
				map[string]interface{}{
					"url":    "https://global.example.com",
					"shards": map[string]interface{}{"matchAll": true},
				},
			},
		},
	}}

	endpoints, err := ListEndpointsFromUnstructured(slice)
	require.NoError(t, err)
	require.Len(t, endpoints, 3)

	require.Equal(t, corev1alpha1.EndpointSelector{}, endpoints[0].Shards,
		"an endpoint without shards keeps prefix matching")

	require.NotNil(t, endpoints[1].Shards.Selector)
	require.Equal(t, map[string]string{"region": "eu"}, endpoints[1].Shards.Selector.MatchLabels)
	require.False(t, endpoints[1].Shards.MatchAll)

	require.True(t, endpoints[2].Shards.MatchAll)
	require.Nil(t, endpoints[2].Shards.Selector)
}

func TestListEndpointsFromUnstructuredRejectsGarbage(t *testing.T) {
	t.Parallel()

	slice := unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"endpoints": []interface{}{
				map[string]interface{}{"url": "https://x.example.com", "shards": "everywhere"},
			},
		},
	}}

	_, err := ListEndpointsFromUnstructured(slice)
	require.ErrorContains(t, err, "expected an object",
		"a shards field that is not a selector must not be silently ignored")
}

func TestFindOneURL(t *testing.T) {
	t.Parallel()

	const thisShard = "https://shard-1.example.com"

	for _, tc := range []struct {
		name    string
		urls    []string
		want    string
		wantErr string
	}{
		{
			name: "one URL per shard, ours among them",
			urls: []string{
				"https://shard-0.example.com/services/replication/a/b",
				"https://shard-1.example.com/services/replication/a/b",
			},
			want: "https://shard-1.example.com/services/replication/a/b",
		},
		{
			name: "a single URL that happens to be ours",
			urls: []string{"https://shard-1.example.com/services/replication/a/b"},
			want: "https://shard-1.example.com/services/replication/a/b",
		},
		{
			// A lone URL is not evidence that it is meant for every shard: it
			// reads the same as another shard's URL, or a stale one. An
			// endpoint that serves the whole installation says so with
			// matchAll, and PickURL honours that before ever getting here.
			name:    "a single URL that is not ours is not adopted",
			urls:    []string{"https://ephemeral.example.com/services/apiexport/a/b"},
			wantErr: "no URLs match prefix",
		},
		{
			name: "several URLs, none of them ours, is not a guess",
			urls: []string{
				"https://shard-0.example.com/services/replication/a/b",
				"https://shard-2.example.com/services/replication/a/b",
			},
			wantErr: "no URLs match prefix",
		},
		{
			name:    "no URLs at all",
			urls:    nil,
			wantErr: "no URLs match prefix",
		},
		{
			name: "two URLs for this shard is ambiguous",
			urls: []string{
				"https://shard-1.example.com/services/replication/a/b",
				"https://shard-1.example.com/services/replication/c/d",
			},
			wantErr: "ambiguous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := FindOneURL(thisShard, tc.urls)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
