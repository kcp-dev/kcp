/*
Copyright 2025 The KCP Authors.

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

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGenCachedObjectName(t *testing.T) {
	tests := map[string]struct {
		gvr       schema.GroupVersionResource
		namespace string
		name      string
		want      string
	}{
		"matching": {
			gvr: schema.GroupVersionResource{
				Group:    "group-1",
				Version:  "v1",
				Resource: "resource-1",
			},
			namespace: "",
			name:      "a-resource",
			want:      "6dgup41jy1ta41gc4q4hb1hdnmrdgl4rdn5ux0t8ya9nhk3jo6",
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			objName := GenCachedObjectName(tt.gvr, tt.namespace, tt.name)
			require.Equal(t, tt.want, objName, "GenCachedObjectName returned an unexpected object name")
		})
	}
}
