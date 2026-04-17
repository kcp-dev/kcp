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

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCacheGVRWithIdentity(t *testing.T) {
	t.Parallel()

	gvr := schema.GroupVersionResource{
		Group:    "wildwest.dev",
		Version:  "v1alpha1",
		Resource: "sheriffs",
	}

	require.Equal(t, schema.GroupVersionResource{
		Group:    "wildwest.dev",
		Version:  "v1alpha1",
		Resource: "sheriffs:identity-hash",
	}, CacheGVRWithIdentity(gvr, "identity-hash"))
}
