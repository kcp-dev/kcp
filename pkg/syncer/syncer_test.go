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

package syncer

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestTransformName(t *testing.T) {
	for _, c := range []struct {
		desc         string
		syncedobject *unstructured.Unstructured
		direction    SyncDirection
		expectedName string
	}{{
		desc:      "Sync kube-root-ca.crt configmap from KCP to a Pcluster",
		direction: SyncDown,
		syncedobject: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"kind":       "ConfigMap",
				"apiVersion": "v1",
				"group":      "",
				"metadata": map[string]interface{}{
					"name": "kube-root-ca.crt",
				},
			},
		},
		expectedName: "kcp-root-ca.crt",
	},
		{
			desc:      "Sync kcp-root-ca.crt configmap from Pcluster to a KCP",
			direction: SyncUp,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ConfigMap",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "kcp-root-ca.crt",
					},
				},
			},
			expectedName: "kube-root-ca.crt",
		},
		{
			desc:      "Sync default serviceaccount from KCP to a Pcluster",
			direction: SyncDown,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ServiceAccount",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "default",
					},
				},
			},
			expectedName: "kcp-default",
		},
		{
			desc:      "Sync kcp-default serviceaccount from Pcluster to a KCP",
			direction: SyncUp,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ServiceAccount",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "kcp-default",
					},
				},
			},
			expectedName: "default",
		},
		{
			desc:      "Sync arbitrary serviceaccount from Pcluster to a KCP",
			direction: SyncUp,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ServiceAccount",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
		{
			desc:      "Sync arbitrary serviceaccount from KCP to a Pcluster",
			direction: SyncDown,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ServiceAccount",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
		{
			desc:      "Sync arbitrary configmap from Pcluster to a KCP",
			direction: SyncUp,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ConfigMap",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
		{
			desc:      "Sync arbitrary configmap from KCP to a Pcluster",
			direction: SyncDown,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "ConfigMap",
					"apiVersion": "v1",
					"group":      "",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
		{
			desc:      "Sync deployment from KCP to a Pcluster",
			direction: SyncDown,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "Deployment",
					"apiVersion": "v1",
					"group":      "apps",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
		{
			desc:      "Sync deployment from Pcluster to KCP",
			direction: SyncUp,
			syncedobject: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "Deployment",
					"apiVersion": "v1",
					"group":      "apps",
					"metadata": map[string]interface{}{
						"name": "arbitrary",
					},
				},
			},
			expectedName: "arbitrary",
		},
	} {
		t.Run(c.desc, func(t *testing.T) {
			transformName(c.syncedobject, c.direction)
			if c.syncedobject.GetName() != c.expectedName {
				t.Fatalf("got %q, want %q", c.syncedobject.GetName(), c.expectedName)
			}
		})
	}
}
