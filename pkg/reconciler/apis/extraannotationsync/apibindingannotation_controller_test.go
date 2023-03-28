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

package extraannotationsync

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestSyncExtraAnnotationPatch(t *testing.T) {
	scenarios := []struct {
		name                  string
		apiExportAnnotations  map[string]string
		apiBindingAnnotations map[string]string
		wantPatch             string
	}{
		{
			name: "nil annotaion",
		},
		{
			name:                  "nothing to patch",
			apiExportAnnotations:  map[string]string{"key1": "value1"},
			apiBindingAnnotations: map[string]string{"key2": "value2"},
		},
		{
			name:                  "sync extra annotations",
			apiExportAnnotations:  map[string]string{"key1": "value1", apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "test1": "test1"},
			apiBindingAnnotations: map[string]string{"key2": "value2", apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "test2": "test2"},
			wantPatch: fmt.Sprintf(
				`{"metadata":{"annotations":{%q:"test1",%q:null}}}`,
				apisv1alpha1.AnnotationAPIExportExtraKeyPrefix+"test1",
				apisv1alpha1.AnnotationAPIExportExtraKeyPrefix+"test2",
			),
		},
		{
			name:                  "update extra annotations",
			apiExportAnnotations:  map[string]string{"key1": "value1", apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "test": "test"},
			apiBindingAnnotations: map[string]string{"key2": "value2", apisv1alpha1.AnnotationAPIExportExtraKeyPrefix + "test": "test1"},
			wantPatch:             fmt.Sprintf(`{"metadata":{"annotations":{%q:"test"}}}`, apisv1alpha1.AnnotationAPIExportExtraKeyPrefix+"test"),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			patch, err := syncExtraAnnotationPatch(scenario.apiExportAnnotations, scenario.apiBindingAnnotations)
			require.NoError(t, err)
			require.Equal(t, scenario.wantPatch, string(patch))
		})
	}
}
