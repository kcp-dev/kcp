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

package builtin

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestInit(t *testing.T) {
	// Note: This test previously checked len(BuiltInAPIs) == len(builtInAPIResourceSchemas).
	// testing for equal length in BuiltInAPIs and builtInAPIResourceSchemas is not enough because
	// BuiltInAPIs has an entry per API version. If a resource is served from multiple API group versions,
	// it will be in that list several times, but only once in the resource schemas (because resource schemas
	// include multiple versions potentially).

	visitedResourceSchemas := map[v1alpha1.GroupResource]bool{}

	for _, api := range BuiltInAPIs {
		gr := v1alpha1.GroupResource{Group: api.GroupVersion.Group, Resource: api.Names.Plural}
		schema, ok := builtInAPIResourceSchemas[gr]
		require.Truef(t, ok, "could not find %s in built-in API resource schemas", api.GroupVersion.String())
		visitedResourceSchemas[gr] = true

		versionFound := false
		for _, version := range schema.Spec.Versions {
			if api.GroupVersion.Version == version.Name {
				versionFound = true
				break
			}
		}
		require.Truef(t, versionFound, "could not find version %s in API resource schema %s", api.GroupVersion.String(), schema.Name)
	}

	require.Equal(t, len(builtInAPIResourceSchemas), len(visitedResourceSchemas))
}
