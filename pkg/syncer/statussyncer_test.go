/*
Copyright 2021 The KCP Authors.

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

package syncer

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestDeepEqualStatus(t *testing.T) {
	for _, c := range []struct {
		desc     string
		old, new *unstructured.Unstructured
		want     bool
	}{{
		desc: "both objects have same status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		want: true,
	}, {
		desc: "both objects have status; different",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "no",
				},
			},
		},
		want: false,
	}, {
		desc: "one object doesn't have status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		want: false,
	}, {
		desc: "both objects don't have status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		want: true,
	}} {
		t.Run(c.desc, func(t *testing.T) {
			got := deepEqualStatus(c.old, c.new)
			if got != c.want {
				t.Fatalf("got %t, want %t", got, c.want)
			}
		})
	}
}
